"""Design feasibility fixture; no daemon imports or product acceptance claim."""

from concurrent import futures
import json
import os
import threading

import grpc
from google.protobuf import empty_pb2, wrappers_pb2
from grpc_health.v1 import health, health_pb2, health_pb2_grpc


def message(value):
    return wrappers_pb2.BytesValue(
        value=json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    )


def session(requests, context):
    yield message({"version": 1, "type": "ready"})
    for request in requests:
        watch = json.loads(request.value.decode("utf-8", errors="strict"))
        if watch["version"] != 1 or watch["type"] != "watch":
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "fixture expected a watch")
        accepted = not watch["watch_args"].get("reject", False)
        result = {"version": 1, "type": "watch_result", "request_id": watch["request_id"], "accepted": accepted}
        if not accepted:
            result["error"] = "fixture rejection"
        yield message(result)
        if accepted:
            # No delay or daemon acknowledgement: order belongs to this stream.
            yield message({"version": 1, "type": "event", "subscription_id": watch["subscription_id"], "context": watch["watch_args"]["context"]})


def serve():
    if os.environ.get("AGENT_PULSE_HUB_PLUGIN") != "plugin-v1":
        raise RuntimeError("fixture must be launched by the go-plugin host")
    stopped = threading.Event()
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    status = health.HealthServicer()
    status.set("plugin", health_pb2.HealthCheckResponse.SERVING)
    health_pb2_grpc.add_HealthServicer_to_server(status, server)

    def shutdown(request, context):
        stopped.set()
        return empty_pb2.Empty()

    # Controller Empty messages are wire-compatible with the library's empty
    # request and response; generated product bindings are unnecessary.
    server.add_generic_rpc_handlers((
        grpc.method_handlers_generic_handler("plugin.GRPCController", {
            "Shutdown": grpc.unary_unary_rpc_method_handler(
                shutdown,
                request_deserializer=empty_pb2.Empty.FromString,
                response_serializer=empty_pb2.Empty.SerializeToString,
            ),
        }),
        grpc.method_handlers_generic_handler("agentpulse.plugin.v1.EventPlugin", {
            "Session": grpc.stream_stream_rpc_method_handler(
                session,
                request_deserializer=wrappers_pb2.BytesValue.FromString,
                response_serializer=wrappers_pb2.BytesValue.SerializeToString,
            ),
        }),
    ))
    port = server.add_insecure_port("127.0.0.1:0")
    if port == 0:
        raise RuntimeError("could not bind the fixture listener")
    server.start()
    print(f"1|1|tcp|127.0.0.1:{port}|grpc", flush=True)
    try:
        if not stopped.wait(timeout=20):
            raise RuntimeError("fixture shutdown deadline exceeded")
    finally:
        server.stop(1).wait(timeout=5)


if __name__ == "__main__":
    serve()
