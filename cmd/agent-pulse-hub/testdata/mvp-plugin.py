"""Independent public Session peer controlled through case-owned files."""

from concurrent import futures
import json
import os
from pathlib import Path
import queue
import sys
import threading
import time

import grpc
from google.protobuf import empty_pb2, wrappers_pb2
from grpc_health.v1 import health, health_pb2, health_pb2_grpc


def serve(directory):
    if os.environ.get("AGENT_PULSE_HUB_PLUGIN") != "plugin-v1":
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

    def watch_reader(requests, context):
        try:
            for request in requests:
                watch = json.loads(request.value.decode("utf-8", errors="strict"))
                if watch.get("version") != 1 or watch.get("type") != "watch":
                    context.abort(grpc.StatusCode.INVALID_ARGUMENT, "fixture expected watch")
                record("watch", frame=watch)
                args = watch["watch_args"]
                gate = args.get("gate")
                while gate and not (directory / gate).exists() and context.is_active() and not stopped.is_set():
                    time.sleep(0.005)
                accepted = not args.get("reject", False)
                result = dict(version=1, type="watch_result", request_id=watch["request_id"], accepted=accepted)
                if not accepted:
                    result["error"] = "fixture rejected arguments"
                outgoing.put(encode(result))
                if "immediate" in args:
                    # Rejected IDs deliberately emit as well, proving no active state.
                    outgoing.put(encode(dict(version=1, type="event", subscription_id=watch["subscription_id"], context=args["immediate"])))
        except grpc.RpcError:
            pass

    def session(requests, context):
        nonlocal active
        with active_lock:
            if active:
                context.abort(grpc.StatusCode.ALREADY_EXISTS, "one Session only")
            active = True
        record("session")
        yield encode(dict(version=1, type="ready"))
        reader = threading.Thread(target=watch_reader, args=(requests, context), daemon=True)
        reader.start()
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
            if command.get("close"):
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
        grpc.method_handlers_generic_handler("agentpulse.plugin.v1.EventPlugin", {
            "Session": grpc.stream_stream_rpc_method_handler(session, request_deserializer=wrappers_pb2.BytesValue.FromString, response_serializer=wrappers_pb2.BytesValue.SerializeToString),
        }),
    ))
    port = server.add_insecure_port("127.0.0.1:0")
    if not port:
        raise RuntimeError("fixture could not bind")
    record("started", pid=os.getpid(), address=f"127.0.0.1:{port}")
    server.start()
    threading.Thread(target=commands, daemon=True).start()
    print(f"1|1|tcp|127.0.0.1:{port}|grpc", flush=True)
    try:
        if not stopped.wait(120):
            raise RuntimeError("fixture exceeded suite lifetime")
    finally:
        server.stop(0).wait(1)
        record("exited", pid=os.getpid())


if __name__ == "__main__":
    serve(sys.argv[1])
