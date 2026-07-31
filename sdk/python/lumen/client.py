"""
Official Python client SDK for the Lumen Event Ingestion Service.
"""

import atexit
import base64
import json
import queue
import threading
import time
import uuid
from typing import Any, Dict, Optional
import requests


def _to_base64(value: Dict[str, Any]) -> str:
    """Encodes a dict as base64-encoded JSON, as required for proto `bytes` fields in Connect's JSON codec."""
    return base64.b64encode(json.dumps(value).encode("utf-8")).decode("ascii")


class Lumen:
    def __init__(
        self,
        ingest_key: str,
        endpoint: str = "http://localhost:50051",
        batch_size: int = 500,
        flush_interval: float = 1.0,
    ):
        if not ingest_key:
            raise ValueError("Lumen: ingest_key is required")

        self.ingest_key = ingest_key
        self.endpoint = endpoint.rstrip("/")
        self.batch_size = batch_size
        self.flush_interval = flush_interval

        self._lock = threading.Lock()
        self.anon_id = str(uuid.uuid4())
        self.user_id = ""
        self.session_id = str(uuid.uuid4())
        now = time.time()
        self.last_activity_at = now
        self.session_start_at = now

        self._queue: queue.Queue = queue.Queue(maxsize=10000)
        self._stop_event = threading.Event()
        self._worker_thread = threading.Thread(target=self._flusher_loop, daemon=True)
        self._worker_thread.start()

        atexit.register(self.close)

    def track(self, name: str, properties: Optional[Dict[str, Any]] = None) -> None:
        """Record a telemetry event. Non-blocking & non-throwing."""
        try:
            if not name:
                return

            with self._lock:
                self._check_session_rotation()
                anon_id = self.anon_id
                user_id = self.user_id
                session_id = self.session_id

            event = {
                "event_id": str(uuid.uuid4()),
                "ts_unix_ms": int(time.time() * 1000),
                "name": name,
                "props_json": _to_base64(properties or {}),
                "overrides": {
                    "anon_id": anon_id,
                    "user_id": user_id,
                    "session_id": session_id,
                    "sdk": "python",
                    "sdk_version": "1.0.0",
                },
            }

            try:
                self._queue.put_nowait(event)
            except queue.Full:
                pass  # Queue overflow drop policy
        except Exception:
            pass  # Non-blocking fail-safe

    def identify(self, user_id: str, traits: Optional[Dict[str, Any]] = None) -> None:
        """Link an anonymous identity to an authenticated user ID."""
        try:
            if not user_id:
                return

            with self._lock:
                self.user_id = user_id
                self._check_session_rotation()
                anon_id = self.anon_id

            payload = {
                "anon_id": anon_id,
                "user_id": user_id,
                "traits_json": _to_base64(traits or {}),
            }

            threading.Thread(
                target=self._send_payload,
                args=("/lumen.v1.IngestService/Identify", payload),
                daemon=True,
            ).start()
        except Exception:
            pass

    def reset(self) -> None:
        """Reset identity state (e.g. on logout)."""
        with self._lock:
            self.user_id = ""
            self.anon_id = str(uuid.uuid4())
            self.session_id = str(uuid.uuid4())
            now = time.time()
            self.last_activity_at = now
            self.session_start_at = now

    def close(self) -> None:
        """Flush pending batches and stop background thread."""
        self._stop_event.set()
        self._flush_batch()

    def _flusher_loop(self) -> None:
        while not self._stop_event.is_set():
            time.sleep(self.flush_interval)
            self._flush_batch()

    def _flush_batch(self) -> None:
        batch = []
        while len(batch) < self.batch_size:
            try:
                item = self._queue.get_nowait()
                batch.append(item)
            except queue.Empty:
                break

        if not batch:
            return

        payload = {
            "context": {
                "sdk": "python",
                "sdk_version": "1.0.0",
            },
            "events": batch,
        }

        self._send_payload("/lumen.v1.IngestService/Track", payload)

    def _send_payload(self, path: str, body: dict) -> None:
        url = f"{self.endpoint}{path}"
        headers = {
            "Content-Type": "application/json",
            "Connect-Protocol-Version": "1",
            "x-lumen-key": self.ingest_key,
        }
        try:
            requests.post(url, headers=headers, json=body, timeout=5)
        except Exception:
            pass

    def _check_session_rotation(self) -> None:
        now = time.time()
        thirty_min = 30 * 60
        twenty_four_hours = 24 * 60 * 60

        if (now - self.last_activity_at > thirty_min) or (now - self.session_start_at > twenty_four_hours):
            self.session_id = str(uuid.uuid4())
            self.session_start_at = now
        self.last_activity_at = now
