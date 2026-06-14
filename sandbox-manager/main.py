import os
import shutil
import tempfile
import json
import logging
from fastapi import FastAPI, UploadFile, File, Form, HTTPException, BackgroundTasks
from fastapi.middleware.cors import CORSMiddleware
import docker
import redis

# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("sandbox-manager")

app = FastAPI(title="IICPC Sandbox Manager")

# Enable CORS for frontend dashboard
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# Initialize Redis client
redis_url = os.getenv("REDIS_URL", "redis://localhost:6379/0")
try:
    r_client = redis.from_url(redis_url)
    logger.info("Connected to Redis successfully.")
except Exception as e:
    logger.error(f"Failed to connect to Redis: {e}")
    r_client = None

# Initialize Docker client
try:
    docker_client = docker.from_env()
    logger.info("Connected to Docker socket successfully.")
except Exception as e:
    logger.error(f"Failed to connect to Docker: {e}")
    docker_client = None

DOCKER_NETWORK = os.getenv("DOCKER_NETWORK", "iicpc-trading-hackathon_benchmarking-net")

# Path to template Dockerfile
DOCKERFILE_TEMPLATE = """FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY main.go .
RUN go mod init matching-engine
RUN CGO_ENABLED=0 GOOS=linux go build -o engine .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/engine .
EXPOSE 8080
CMD ["./engine"]
"""

@app.get("/health")
def health():
    return {"status": "ok", "docker": docker_client is not None, "redis": r_client is not None}

def sanitize_team_name(team_name: str) -> str:
    cleaned = team_name.strip()
    if not all(c.isalnum() or c in ('-', '_', ' ') for c in cleaned):
        raise HTTPException(status_code=400, detail="Team name must be alphanumeric, dashes (-), underscores (_), or spaces")
    # Replace spaces with dashes, convert to lowercase
    sanitized = cleaned.replace(" ", "-").lower()
    while "--" in sanitized:
        sanitized = sanitized.replace("--", "-")
    return sanitized

@app.post("/submit")
async def submit_code(team_name: str = Form(...), file: UploadFile = File(...)):
    if not docker_client:
        raise HTTPException(status_code=500, detail="Docker client not available on host")

    team_id = sanitize_team_name(team_name)

    # Create temporary directory to build the image
    with tempfile.TemporaryDirectory() as tmpdir:
        # Save uploaded file
        main_go_path = os.path.join(tmpdir, "main.go")
        with open(main_go_path, "wb") as f:
            f.write(await file.read())

        # Write Dockerfile template
        dockerfile_path = os.path.join(tmpdir, "Dockerfile")
        with open(dockerfile_path, "w") as f:
            f.write(DOCKERFILE_TEMPLATE)

        image_tag = f"iicpc-contestant-{team_id}"
        logger.info(f"Building Docker image {image_tag} for team {team_id}...")

        try:
            image, build_logs = docker_client.images.build(
                path=tmpdir,
                tag=image_tag,
                rm=True
            )
            logger.info(f"Successfully built image {image_tag}")
            return {"status": "success", "message": f"Image {image_tag} built successfully", "tag": image_tag}
        except docker.errors.BuildError as e:
            logger.error(f"Build error: {e}")
            # Format build log for error details
            log_str = ""
            for log in e.build_log:
                if 'stream' in log:
                    log_str += log['stream']
            raise HTTPException(status_code=400, detail={"error": "Build failed", "logs": log_str})
        except Exception as e:
            logger.error(f"Unexpected build error: {e}")
            raise HTTPException(status_code=500, detail=str(e))

@app.post("/start/{team_name}")
def start_sandbox(team_name: str, cpus: float = 0.5, memory_mb: int = 256):
    if not docker_client:
        raise HTTPException(status_code=500, detail="Docker client not available")

    team_id = sanitize_team_name(team_name)
    container_name = f"iicpc-contestant-{team_id}"
    image_tag = f"iicpc-contestant-{team_id}"

    # Stop any existing container
    try:
        existing = docker_client.containers.get(container_name)
        logger.info(f"Removing existing container {container_name}")
        existing.remove(force=True)
    except docker.errors.NotFound:
        pass

    # Start container with resource limits and attach to network
    nano_cpus = int(cpus * 1_000_000_000)
    mem_limit = f"{memory_mb}m"

    logger.info(f"Starting sandbox container {container_name} with cpus={cpus}, memory={mem_limit}")

    try:
        container = docker_client.containers.run(
            image_tag,
            detach=True,
            name=container_name,
            hostname=container_name,
            nano_cpus=nano_cpus,
            mem_limit=mem_limit,
            network=DOCKER_NETWORK,
            restart_policy={"Name": "on-failure", "MaximumRetryCount": 3}
        )
        return {"status": "running", "container_id": container.short_id, "endpoint": f"http://{container_name}:8080"}
    except docker.errors.ImageNotFound:
        raise HTTPException(status_code=404, detail="Team submission image not found. Submit code first.")
    except Exception as e:
        logger.error(f"Failed to start container: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.post("/stop/{team_name}")
def stop_sandbox(team_name: str):
    if not docker_client:
        raise HTTPException(status_code=500, detail="Docker client not available")

    team_id = sanitize_team_name(team_name)
    container_name = f"iicpc-contestant-{team_id}"
    try:
        container = docker_client.containers.get(container_name)
        container.stop(timeout=5)
        container.remove()
        logger.info(f"Stopped and removed container {container_name}")
        return {"status": "stopped", "message": f"Sandbox for {team_name} stopped"}
    except docker.errors.NotFound:
        return {"status": "not_found", "message": "Container not running"}
    except Exception as e:
        logger.error(f"Failed to stop container: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/status/{team_name}")
def sandbox_status(team_name: str):
    if not docker_client:
        raise HTTPException(status_code=500, detail="Docker client not available")

    team_id = sanitize_team_name(team_name)
    container_name = f"iicpc-contestant-{team_id}"
    try:
        container = docker_client.containers.get(container_name)
        return {"status": "running", "state": container.status}
    except docker.errors.NotFound:
        return {"status": "not_running"}

@app.post("/start-test")
def start_test(team_name: str = Form(...), bot_count: int = Form(50), duration_seconds: int = Form(30)):
    if not r_client:
        raise HTTPException(status_code=500, detail="Redis client not available")

    team_id = sanitize_team_name(team_name)

    # Enqueue load test job
    job = {
        "team_name": team_id,
        "target_url": f"http://iicpc-contestant-{team_id}:8080",
        "bot_count": bot_count,
        "duration": duration_seconds
    }

    # Publish start command to Redis pub/sub or list queue
    try:
        # Push to job queue
        r_client.lpush("test_jobs", json.dumps(job))
        # Publish start notification
        r_client.publish("test_events", json.dumps({"event": "queued", "team_name": team_id}))
        logger.info(f"Queued stress test for team {team_id} with {bot_count} bots for {duration_seconds}s")
        return {"status": "queued", "job": job}
    except Exception as e:
        logger.error(f"Failed to enqueue job: {e}")
        raise HTTPException(status_code=500, detail=str(e))
