"""A standard health server fixture, with no product plugin API."""

from concurrent import futures
import os
import threading

import grpc
from google.protobuf import empty_pb2
from grpc_health.v1 import health, health_pb2, health_pb2_grpc


def serve():
    if os.environ.get("APH_TOOLING_PLUGIN") != "qualification":
        raise RuntimeError("fixture must be launched by the qualification host")
    stopped = threading.Event()
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    status = health.HealthServicer()
    status.set("plugin", health_pb2.HealthCheckResponse.SERVING)
    health_pb2_grpc.add_HealthServicer_to_server(status, server)

    def shutdown(request, context):
        stopped.set()
        return empty_pb2.Empty()

    # go-plugin v1.8.0 internal/plugin/grpc_controller.proto defines empty
    # request/response messages, wire-compatible with google.protobuf.Empty.
    server.add_generic_rpc_handlers((
        grpc.method_handlers_generic_handler("plugin.GRPCController", {
            "Shutdown": grpc.unary_unary_rpc_method_handler(
                shutdown,
                request_deserializer=empty_pb2.Empty.FromString,
                response_serializer=empty_pb2.Empty.SerializeToString,
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
