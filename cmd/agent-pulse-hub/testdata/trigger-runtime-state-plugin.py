"""Verification-only participating input for trigger runtime-state acceptance."""

from concurrent import futures
import json
import os
from pathlib import Path
import queue
import threading
import time

import grpc
from google.protobuf import empty_pb2, wrappers_pb2
from grpc_health.v1 import health, health_pb2, health_pb2_grpc


SCHEMA = "probe-v1"


def serve(directory):
    if os.environ.get("AGENT_PULSE_HUB_PLUGIN") != "plugin-v2":
        raise RuntimeError("fixture requires the framework launch cookie")
    directory = Path(directory)
    stopped = threading.Event()
    outgoing = queue.Queue()
    journal_lock = threading.Lock()
    state_lock = threading.RLock()
    watches = {}
    active_lock = threading.Lock()
    active = False

    def record(kind, **fields):
        with journal_lock:
            row = dict(kind=kind, time=time.time(), pid=os.getpid(), **fields)
            with (directory / "journal.jsonl").open("a", encoding="utf-8") as stream:
                stream.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")

    def encode(frame):
        return wrappers_pb2.BytesValue(value=json.dumps(frame, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))

    def state_for(watch):
        return {"schema": SCHEMA, "payload": {"confirmed": list(watch["confirmed"])}}

    def read_source(watch):
        position = 0
        source = Path(watch["args"]["source_file"])
        while not stopped.is_set():
            with state_lock:
                if watch["quiesced"]:
                    return
            try:
                with source.open("r", encoding="utf-8") as stream:
                    stream.seek(position)
                    line = stream.readline()
                    if line and not line.endswith("\n"):
                        line = ""
                    if line:
                        position = stream.tell()
            except FileNotFoundError:
                line = ""
            if not line:
                stopped.wait(0.01)
                continue
            try:
                update = json.loads(line)
                update_id = update["id"]
                context = update["context"]
                if not isinstance(update_id, str) or not update_id or not isinstance(context, str) or not context:
                    raise ValueError("source update requires nonempty id and context")
            except Exception as error:
                record("source_error", subscription_id=watch["subscription_id"], reason=str(error)[:256])
                continue
            with state_lock:
                if watch["quiesced"] or stopped.is_set():
                    return
                if update_id in watch["confirmed"]:
                    record("suppressed", subscription_id=watch["subscription_id"], update_id=update_id)
                    continue
                watch["event_id"] += 1
                event_id = str(watch["event_id"])
                watch["pending"][event_id] = update_id
                frame = {
                    "version": 2,
                    "type": "state_event",
                    "subscription_id": watch["subscription_id"],
                    "event_id": event_id,
                    "context": context,
                }
                record("state_event", subscription_id=watch["subscription_id"], event_id=event_id, update_id=update_id)
                outgoing.put(encode(frame))

    def reader(requests, context):
        try:
            for request in requests:
                frame = json.loads(request.value.decode("utf-8", errors="strict"))
                kind = frame.get("type")
                if frame.get("version") != 2 or kind not in ("state_watch", "delivery_notice", "checkpoint"):
                    context.abort(grpc.StatusCode.INVALID_ARGUMENT, "fixture received an unsupported message")
                subscription_id = frame.get("subscription_id", "")
                if kind == "state_watch":
                    state = frame.get("state")
                    record("state_watch", subscription_id=subscription_id, state=state, watch_args=frame.get("watch_args"))
                    accepted_state = state is None or (isinstance(state, dict) and state.get("schema") == SCHEMA and isinstance(state.get("payload"), dict))
                    if not accepted_state:
                        record("watch_rejected", subscription_id=subscription_id, reason="incompatible state schema")
                        outgoing.put(encode({"version": 2, "type": "watch_result", "request_id": frame["request_id"], "accepted": False, "error": "incompatible probe state schema"}))
                        continue
                    payload = {} if state is None else state["payload"]
                    confirmed = payload.get("confirmed", [])
                    if not isinstance(confirmed, list) or any(not isinstance(item, str) or not item for item in confirmed):
                        record("watch_rejected", subscription_id=subscription_id, reason="invalid confirmed update list")
                        outgoing.put(encode({"version": 2, "type": "watch_result", "request_id": frame["request_id"], "accepted": False, "error": "invalid probe progress state"}))
                        continue
                    watch = {
                        "subscription_id": subscription_id,
                        "args": frame["watch_args"],
                        "confirmed": list(dict.fromkeys(confirmed)),
                        "pending": {},
                        "event_id": 0,
                        "quiesced": False,
                    }
                    outgoing.put(encode({"version": 2, "type": "watch_result", "request_id": frame["request_id"], "accepted": True}))
                    with state_lock:
                        watches[subscription_id] = watch
                    record("watch_started", subscription_id=subscription_id)
                    threading.Thread(target=read_source, args=(watch,), daemon=True).start()
                elif kind == "delivery_notice":
                    with state_lock:
                        watch = watches.get(subscription_id)
                        update_id = None if watch is None else watch["pending"].pop(frame["event_id"], None)
                        status = frame.get("status")
                        if watch is not None and update_id is not None and status == "accepted" and update_id not in watch["confirmed"]:
                            watch["confirmed"].append(update_id)
                        record("delivery_notice", subscription_id=subscription_id, event_id=frame.get("event_id"), update_id=update_id, status=status)
                else:
                    with state_lock:
                        watch = watches.get(subscription_id)
                        if watch is None:
                            outgoing.put(encode({"version": 2, "type": "checkpoint_result", "request_id": frame["request_id"], "accepted": False, "error": "unknown subscription"}))
                            continue
                        if frame["quiesce"]:
                            watch["quiesced"] = True
                        snapshot = state_for(watch)
                        record("checkpoint", subscription_id=subscription_id, quiesce=frame["quiesce"], state=snapshot)
                        outgoing.put(encode({"version": 2, "type": "checkpoint_result", "request_id": frame["request_id"], "accepted": True, "state": snapshot}))
        except grpc.RpcError:
            pass
        except Exception as error:
            record("fixture_error", reason=str(error)[:256])

    def session(requests, context):
        nonlocal active
        with active_lock:
            if active:
                context.abort(grpc.StatusCode.ALREADY_EXISTS, "one Session only")
            active = True
        record("session")
        yield encode({"version": 2, "type": "ready", "role": "input", "capabilities": ["runtime_state_v1"]})
        threading.Thread(target=reader, args=(requests, context), daemon=True).start()
        try:
            while context.is_active() and not stopped.is_set():
                try:
                    item = outgoing.get(timeout=0.02)
                except queue.Empty:
                    continue
                if item is None:
                    return
                yield item
        finally:
            record("session_closed")
            with active_lock:
                active = False

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    health_status = health.HealthServicer()
    health_status.set("plugin", health_pb2.HealthCheckResponse.SERVING)
    health_pb2_grpc.add_HealthServicer_to_server(health_status, server)

    def shutdown(_request, _context):
        record("controller_shutdown")
        stopped.set()
        return empty_pb2.Empty()

    server.add_generic_rpc_handlers((
        grpc.method_handlers_generic_handler("plugin.GRPCController", {
            "Shutdown": grpc.unary_unary_rpc_method_handler(shutdown, request_deserializer=empty_pb2.Empty.FromString, response_serializer=empty_pb2.Empty.SerializeToString),
        }),
        grpc.method_handlers_generic_handler("agentpulse.plugin.v2.Plugin", {
            "Session": grpc.stream_stream_rpc_method_handler(session, request_deserializer=wrappers_pb2.BytesValue.FromString, response_serializer=wrappers_pb2.BytesValue.SerializeToString),
        }),
    ))
    port = server.add_insecure_port("127.0.0.1:0")
    if not port:
        raise RuntimeError("fixture could not bind")
    record("started", address=f"127.0.0.1:{port}")
    server.start()
    print(f"1|2|tcp|127.0.0.1:{port}|grpc", flush=True)
    try:
        if not stopped.wait(120):
            raise RuntimeError("fixture exceeded suite lifetime")
    finally:
        server.stop(0).wait(1)
        record("exited")


if __name__ == "__main__":
    import sys
    serve(sys.argv[1])
