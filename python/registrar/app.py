import logging

from fastapi import FastAPI, Query

from log_buffer import InMemoryLogBuffer
from scheduler import RegistrarScheduler
from settings import load_settings

settings = load_settings()
logging.basicConfig(
	level=getattr(logging, settings.log_level.upper(), logging.INFO),
	format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
log_buffer = InMemoryLogBuffer(capacity=5000)
log_buffer.setFormatter(logging.Formatter("%(asctime)s [%(levelname)s] %(name)s: %(message)s"))
logging.getLogger().addHandler(log_buffer)

app = FastAPI(title="Gemini Registrar", version="1.0.0")
scheduler = RegistrarScheduler(settings)


@app.on_event("startup")
def on_startup() -> None:
	scheduler.start()


@app.on_event("shutdown")
def on_shutdown() -> None:
	scheduler.stop()


@app.get("/health")
def health() -> dict:
	return {"status": "ok", "service": "registrar"}


@app.get("/metrics")
def metrics() -> dict:
	return scheduler.snapshot()


@app.post("/trigger/register")
def trigger_register(count: int = Query(1, ge=1, le=20)) -> dict:
	return scheduler.trigger_register(count=count)


@app.post("/trigger/refresh")
def trigger_refresh(limit: int = Query(20, ge=1, le=200)) -> dict:
	return scheduler.trigger_refresh(limit=limit)


@app.get("/logs")
def logs(
	after_id: int = Query(0, ge=0),
	limit: int = Query(200, ge=1, le=1000),
	level: str = Query("all"),
) -> dict:
	return log_buffer.snapshot(after_id=after_id, limit=limit, level=level)
