"""Storage-free protocol feasibility peer; not a shipped trigger or acceptance."""

from concurrent import futures
import json
import os
import queue
import threading

import grpc
from google.protobuf import empty_pb2, wrappers_pb2
from grpc_health.v1 import health, health_pb2, health_pb2_grpc


def serve():
    if os.environ.get("AGENT_PULSE_HUB_PLUGIN") != "plugin-v2":
        raise RuntimeError("fixture requires the framework launch cookie")
    stopped = threading.Event()
    outgoing = queue.Queue()
    subscription = None
    accepted_updates = []
    updates = {"1": "PROBE-U1", "2": "PROBE-U2"}

    def encode(frame):
        return wrappers_pb2.BytesValue(value=json.dumps(frame, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))

    def receive(requests, context):
        nonlocal subscription, accepted_updates
        try:
            for request in requests:
                frame = json.loads(request.value.decode("utf-8", errors="strict"))
                kind = frame.get("type")
                if frame.get("version") != 2:
                    context.abort(grpc.StatusCode.INVALID_ARGUMENT, "wrong version")
                if kind == "state_watch":
                    subscription = frame["subscription_id"]
                    state = frame["state"]
                    accepted_updates = [] if state is None else list(state["payload"]["accepted"])
                    outgoing.put(dict(version=2, type="watch_result", request_id=frame["request_id"], accepted=True))
                    for event_id, update in updates.items():
                        outgoing.put(dict(version=2, type="state_event", subscription_id=subscription, event_id=event_id, context=update))
                elif kind == "delivery_notice":
                    if frame["subscription_id"] != subscription or frame["event_id"] not in updates:
                        context.abort(grpc.StatusCode.INVALID_ARGUMENT, "notice correlation")
                    if frame["status"] == "accepted":
                        accepted_updates.append(updates[frame["event_id"]])
                elif kind == "checkpoint":
                    if frame["subscription_id"] != subscription:
                        context.abort(grpc.StatusCode.INVALID_ARGUMENT, "checkpoint correlation")
                    outgoing.put(dict(version=2, type="checkpoint_result", request_id=frame["request_id"], accepted=True,
                                      state=dict(schema="probe-v1", payload=dict(accepted=list(accepted_updates)))))
                else:
                    context.abort(grpc.StatusCode.INVALID_ARGUMENT, "unexpected probe operation")
        finally:
            outgoing.put(None)

    def session(requests, context):
        yield encode(dict(version=2, type="ready", role="input", capabilities=["runtime_state_v1"]))
        threading.Thread(target=receive, args=(requests, context), daemon=True).start()
        while context.is_active() and not stopped.is_set():
            try:
                frame = outgoing.get(timeout=0.02)
            except queue.Empty:
                continue
            if frame is None:
                return
            yield encode(frame)

    def shutdown(request, context):
        stopped.set()
        return empty_pb2.Empty()

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    health_service = health.HealthServicer()
    health_service.set("plugin", health_pb2.HealthCheckResponse.SERVING)
    health_pb2_grpc.add_HealthServicer_to_server(health_service, server)
    server.add_generic_rpc_handlers((grpc.method_handlers_generic_handler("agentpulse.plugin.v2.Plugin", {
        "Session": grpc.stream_stream_rpc_method_handler(session, request_deserializer=wrappers_pb2.BytesValue.FromString,
                                                         response_serializer=wrappers_pb2.BytesValue.SerializeToString)
    }), grpc.method_handlers_generic_handler("plugin.GRPCController", {
        "Shutdown": grpc.unary_unary_rpc_method_handler(shutdown, request_deserializer=empty_pb2.Empty.FromString,
                                                       response_serializer=empty_pb2.Empty.SerializeToString)
    })))
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    print(f"1|2|tcp|127.0.0.1:{port}|grpc|", flush=True)
    stopped.wait()
    server.stop(0.2).wait()


if __name__ == "__main__":
    serve()
