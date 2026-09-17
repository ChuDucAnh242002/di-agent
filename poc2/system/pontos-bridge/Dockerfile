FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY main.py .

# Unbuffered stdout: print() otherwise sits in a block buffer and never
# shows up in `kubectl logs` until enough output accumulates.
ENV PYTHONUNBUFFERED=1 \
    KAFKA_BROKERS=localhost:9092 \
    KAFKA_TOPIC=pontos.telemetry \
    PONTOS_HOST=pontos.ri.se \
    PONTOS_PORT=443 \
    PONTOS_PATH=/mqtt \
    PONTOS_USERNAME=__token__ \
    PONTOS_TOKEN= \
    PONTOS_SUBSCRIBE_TOPIC=PONTOS_EGRESS/# \
    RECONNECT_BACKOFF_S=5 \
    LOG_EVERY_N_MESSAGES=50

CMD ["python", "main.py"]
