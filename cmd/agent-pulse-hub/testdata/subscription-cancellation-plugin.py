"""SC case-owned input: source/journal/gates only; the daemon owns all storage."""

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


def serve(directory):
    if os.environ.get("AGENT_PULSE_HUB_PLUGIN") != "plugin-v2":
        raise RuntimeError("fixture requires framework launch cookie")
    directory = Path(directory)
    stopped = threading.Event()
    outgoing = queue.Queue()
    lock = threading.RLock()
    journal_lock = threading.Lock()
    watches = {}
    used = set()

    def record(kind, **fields):
        with journal_lock:
            with (directory / "journal.jsonl").open("a", encoding="utf-8") as stream:
                stream.write(json.dumps(dict(kind=kind, pid=os.getpid(), time=time.time(), **fields), ensure_ascii=False) + "\n")

    def send(frame):
        outgoing.put(wrappers_pb2.BytesValue(value=json.dumps(frame, ensure_ascii=False, separators=(",", ":")).encode("utf-8")))

    def result(frame, kind, accepted=True, **extra):
        send(dict(version=2, type=kind, request_id=frame["request_id"], accepted=accepted, **extra))

    def source(watch):
        position = 0
        try:
            while not stopped.is_set():
                with lock:
                    if watch["quiesced"]:
                        return
                try:
                    with Path(watch["args"]["source_file"]).open("r", encoding="utf-8") as stream:
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
                update = json.loads(line)
                with lock:
                    if watch["quiesced"] or stopped.is_set():
                        return
                    if update["id"] in watch["confirmed"]:
                        record("suppressed", subscription_id=watch["id"], update_id=update["id"])
                        continue
                    watch["next"] += 1
                    event_id = str(watch["next"])
                    watch["pending"][event_id] = update["id"]
                    record("state_event", subscription_id=watch["id"], event_id=event_id, update_id=update["id"])
                    send(dict(version=2, type="state_event", subscription_id=watch["id"], event_id=event_id, context=update["context"]))
        finally:
            watch["done"].set()

    def cancel(frame):
        sid = frame["subscription_id"]
        record("cancel_watch", subscription_id=sid, request_id=frame["request_id"])
        control_path = directory / "cancel-control.json"
        control = json.loads(control_path.read_text(encoding="utf-8")) if control_path.exists() else {}
        if control.get("subscription_id") == sid:
            if control.get("reject"):
                record("cancel_rejected", subscription_id=sid)
                result(frame, "cancel_result", False, error="controlled cancellation rejection")
                return
            gate = control.get("release_file")
            if gate:
                record("cancel_held", subscription_id=sid)
                while not stopped.is_set() and not Path(gate).exists():
                    stopped.wait(0.005)
                if stopped.is_set():
                    return
        with lock:
            watch = watches.get(sid)
            if watch is not None:
                watch["quiesced"] = True
        if watch is not None and not watch["done"].wait(2):
            result(frame, "cancel_result", False, error="source failed to stop")
            return
        with lock:
            watches.pop(sid, None)
        record("watch_stopped", subscription_id=sid)
        result(frame, "cancel_result")

    def reader(requests, context):
        try:
            for request in requests:
                frame = json.loads(request.value.decode("utf-8", errors="strict"))
                kind, sid = frame["type"], frame.get("subscription_id")
                if frame.get("version") != 2:
                    context.abort(grpc.StatusCode.INVALID_ARGUMENT, "wrong version")
                if kind == "cancel_watch":
                    threading.Thread(target=cancel, args=(frame,), daemon=True).start()
                    continue
                with lock:
                    if kind == "state_watch":
                        record("state_watch", subscription_id=sid, state=frame["state"], watch_args=frame["watch_args"])
                        if sid in used or any(w["args"] == frame["watch_args"] for w in watches.values()):
                            result(frame, "watch_result", False, error="source resource is already reserved")
                            continue
                        state = frame["state"]
                        if state is not None and state.get("schema") != "probe-v1":
                            result(frame, "watch_result", False, error="incompatible source state")
                            continue
                        watch = dict(id=sid, args=frame["watch_args"], confirmed=[] if state is None else list(state["payload"]["confirmed"]), pending={}, next=0, quiesced=False, done=threading.Event())
                        used.add(sid)
                        watches[sid] = watch
                        result(frame, "watch_result")
                        record("watch_started", subscription_id=sid)
                        threading.Thread(target=source, args=(watch,), daemon=True).start()
                    elif kind == "delivery_notice":
                        watch = watches.get(sid)
                        update = None if watch is None else watch["pending"].pop(frame["event_id"], None)
                        if watch is not None and update is not None and frame["status"] == "accepted" and update not in watch["confirmed"]:
                            watch["confirmed"].append(update)
                        record("delivery_notice", subscription_id=sid, update_id=update, event_id=frame["event_id"], status=frame["status"])
                    elif kind == "checkpoint":
                        watch = watches.get(sid)
                        if watch is None:
                            result(frame, "checkpoint_result", False, error="unknown subscription")
                            continue
                        if frame["quiesce"]:
                            watch["quiesced"] = True
                        state = dict(schema="probe-v1", payload=dict(confirmed=list(watch["confirmed"])))
                        record("checkpoint", subscription_id=sid, quiesce=frame["quiesce"], state=state)
                        result(frame, "checkpoint_result", state=state)
                    else:
                        context.abort(grpc.StatusCode.INVALID_ARGUMENT, "unsupported input message")
        except grpc.RpcError:
            pass
        except Exception as error:
            record("fixture_error", reason=str(error))

    def commands():
        number = 1
        while not stopped.is_set():
            path = directory / f"command-{number}.json"
            # The publisher releases this read after its rename has returned.
            if not (directory / f"command-{number}.ready").exists():
                stopped.wait(0.005)
                continue
            command = json.loads(path.read_text(encoding="utf-8"))
            record("command", number=number)
            number += 1
            if command.get("exit"):
                os._exit(7)
            send(command)

    def session(requests, context):
        record("session")
        yield wrappers_pb2.BytesValue(value=json.dumps(dict(version=2, type="ready", role="input", capabilities=["runtime_state_v1", "cancel_watch_v1"])).encode())
        threading.Thread(target=reader, args=(requests, context), daemon=True).start()
        try:
            while context.is_active() and not stopped.is_set():
                try:
                    yield outgoing.get(timeout=0.02)
                except queue.Empty:
                    pass
        finally:
            record("session_closed")

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    health_service = health.HealthServicer()
    health_service.set("plugin", health_pb2.HealthCheckResponse.SERVING)
    health_pb2_grpc.add_HealthServicer_to_server(health_service, server)

    def shutdown(_request, _context):
        record("controller_shutdown")
        stopped.set()
        return empty_pb2.Empty()

    server.add_generic_rpc_handlers((
        grpc.method_handlers_generic_handler("plugin.GRPCController", {"Shutdown": grpc.unary_unary_rpc_method_handler(shutdown, request_deserializer=empty_pb2.Empty.FromString, response_serializer=empty_pb2.Empty.SerializeToString)}),
        grpc.method_handlers_generic_handler("agentpulse.plugin.v2.Plugin", {"Session": grpc.stream_stream_rpc_method_handler(session, request_deserializer=wrappers_pb2.BytesValue.FromString, response_serializer=wrappers_pb2.BytesValue.SerializeToString)}),
    ))
    port = server.add_insecure_port("127.0.0.1:0")
    record("started", address=f"127.0.0.1:{port}")
    server.start()
    threading.Thread(target=commands, daemon=True).start()
    print(f"1|2|tcp|127.0.0.1:{port}|grpc", flush=True)
    try:
        if not stopped.wait(120):
            raise RuntimeError("fixture lifetime exceeded")
    finally:
        server.stop(0).wait(1)
        record("exited")


if __name__ == "__main__":
    import sys
    serve(sys.argv[1])
