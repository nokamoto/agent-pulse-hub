"""ND-owned output peer; replaces destination timing/results, never hub policy."""

import argparse
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


def serve(directory, role):
    if os.environ.get("AGENT_PULSE_HUB_PLUGIN") != "plugin-v2":
        raise RuntimeError("fixture requires the framework launch cookie")
    directory = Path(directory)
    stopped = threading.Event()
    outgoing = queue.Queue()
    journal_lock = threading.Lock()
    active_lock = threading.Lock()
    active = False

    def record(kind, **fields):
        with journal_lock:
            with (directory / "journal.jsonl").open("a", encoding="utf-8") as stream:
                stream.write(json.dumps(dict(kind=kind, time=time.time(), **fields), ensure_ascii=False) + "\n")

    def encode(value):
        return wrappers_pb2.BytesValue(value=json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))

    def reader(requests, context):
        try:
            for request in requests:
                frame = json.loads(request.value.decode("utf-8", errors="strict"))
                kind = frame.get("type")
                allowed = ("watch",) if role == "input" else ("validate_destination", "notification_deliver")
                if frame.get("version") != 2 or kind not in allowed:
                    context.abort(grpc.StatusCode.INVALID_ARGUMENT, "fixture received wrong role or version")
                record(kind, frame=frame)
                args = frame["watch_args"] if role == "input" else frame["destination_args"]
                gate = args.get("gate") if kind == "notification_deliver" else None
                while gate and not (directory / gate).exists() and context.is_active() and not stopped.is_set():
                    time.sleep(0.005)
                if kind == "notification_deliver":
                    if args.get("manual"):
                        continue
                    rows = [json.loads(line) for line in (directory / "journal.jsonl").read_text(encoding="utf-8").splitlines()]
                    ordinal = sum(row.get("kind") == kind and row.get("frame", {}).get("delivery_id") == frame["delivery_id"] for row in rows)
                    statuses = args.get("statuses", ["accepted"])
                    outcome = statuses[min(ordinal - 1, len(statuses) - 1)]
                    result = dict(version=2, type="notification_result", request_id=frame["request_id"], delivery_id=frame["delivery_id"], status=outcome)
                    if outcome == "accepted":
                        result["receipt"] = "fixture-queue-" + str(ordinal)
                    else:
                        result.update(reason="controlled_outcome", message="Controlled destination outcome.")
                        if outcome == "unsubmitted":
                            result["retryable"] = args.get("retryable", True)
                            if result["retryable"] and "retry_after_ms" in args:
                                result["retry_after_ms"] = args["retry_after_ms"]
                else:
                    accepted = not args.get("reject", False)
                    result = dict(version=2, type="watch_result" if kind == "watch" else "destination_result", request_id=frame["request_id"], accepted=accepted)
                    if not accepted:
                        result["error"] = "fixture rejected arguments"
                record("result", frame=result)
                outgoing.put(encode(result))
                if kind == "watch" and "immediate" in args:
                    # A rejected ID may emit too, proving admission stays closed.
                    outgoing.put(encode(dict(version=2, type="event", subscription_id=frame["subscription_id"], context=args["immediate"])))
        except grpc.RpcError:
            pass

    def session(requests, context):
        nonlocal active
        with active_lock:
            if active:
                context.abort(grpc.StatusCode.ALREADY_EXISTS, "one Session only")
            active = True
        record("session")
        yield encode(dict(version=2, type="ready", role=role, capabilities=["notification_delivery_v1"]))
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

    def commands():
        number = 1
        while not stopped.is_set():
            path = directory / f"command-{number}.json"
            if not path.exists():
                time.sleep(0.005)
                continue
            command = json.loads(path.read_text(encoding="utf-8"))
            record("command", number=number, command=command)
            number += 1
            if command.get("exit"):
                # Abrupt peer exit qualifies daemon process-failure wiring;
                # no graceful shutdown or future restart is claimed.
                os._exit(7)
            elif command.get("close"):
                outgoing.put(None)
            elif "raw" in command:
                outgoing.put(wrappers_pb2.BytesValue(value=command["raw"].encode("utf-8")))
            else:
                outgoing.put(encode(command))

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    status = health.HealthServicer()
    status.set("plugin", health_pb2.HealthCheckResponse.SERVING)
    health_pb2_grpc.add_HealthServicer_to_server(status, server)

    def shutdown(request, context):
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
    record("started", pid=os.getpid(), address=f"127.0.0.1:{port}", role=role)
    server.start()
    threading.Thread(target=commands, daemon=True).start()
    print(f"1|2|tcp|127.0.0.1:{port}|grpc", flush=True)
    try:
        if not stopped.wait(120):
            raise RuntimeError("fixture exceeded suite lifetime")
    finally:
        server.stop(0).wait(1)
        record("exited", pid=os.getpid())


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("directory")
    parser.add_argument("--role", choices=("input", "output"), required=True)
    options = parser.parse_args()
    serve(options.directory, options.role)
