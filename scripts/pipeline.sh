#!/usr/bin/env bash
#
# LLM Training Pipeline
#
# Fully automated: export conversation data from the records bot's database,
# send it to a Windows desktop for QLoRA fine-tuning, then deploy the
# resulting GGUF model to the Ollama instance on server0.
#
# All configuration lives in $HOME/.pipeline.env (no CLI arguments).
# Designed to be run as a weekly cron job on server1.
#
# Usage:
#   ./pipeline.sh
#
# Prerequisites:
#   - $HOME/.pipeline.env exists with all required variables
#   - SSH key auth configured for WINDOWS_HOST and SERVER0
#   - Windows OpenSSH Server enabled with Python + Unsloth installed
#   - Ollama running on server0

set -euo pipefail

ENV_FILE="${HOME}/.pipeline.env"
if [ ! -f "$ENV_FILE" ]; then
    echo "Error: $ENV_FILE not found. Create it with the required variables."
    echo "See the README for the template."
    exit 1
fi

# Export all variables so child scripts (deploy.sh) inherit them.
set -a
# shellcheck source=/dev/null
source "$ENV_FILE"
set +a

# --- Validate required variables ---
: "${TARGET_GUILD_ID:?TARGET_GUILD_ID is required in $ENV_FILE}"
: "${WINDOWS_HOST:?WINDOWS_HOST is required in $ENV_FILE}"
: "${SERVER0:?SERVER0 is required in $ENV_FILE}"

BOT_DISPLAY_NAME="${BOT_DISPLAY_NAME:-Discord User}"

WINDOWS_TRAIN_DIR="${WINDOWS_TRAIN_DIR:-C:/training}"
TRAIN_VENV_ACTIVATE="${TRAIN_VENV_ACTIVATE:-C:/training/venv/Scripts/activate}"
TRAIN_SCRIPT_PATH="${TRAIN_SCRIPT_PATH:-C:/training/train.py}"
SERVER0_MODELS_DIR="${SERVER0_MODELS_DIR:-/home/admin/models}"
OLLAMA_CONTAINER="${OLLAMA_CONTAINER:-discord-quotes-bot-ollama-1}"

BASE_MODEL="${BASE_MODEL:-unsloth/Llama-3.2-3B-Instruct-bnb-4bit}"
EPOCHS="${EPOCHS:-3}"
QUANT_METHOD="${QUANT_METHOD:-q4_k_m}"

MODEL_NAME="impersonate"
PIPELINE_DIR="/mnt/raid0/discord-records-bot/pipeline"
CURSOR_FILE="${PIPELINE_DIR}/.last_export"
DATA_FILE="${PIPELINE_DIR}/training_data.jsonl"
GGUF_LOCAL="${PIPELINE_DIR}/model.gguf"

BOT_CONTAINER="discord-records-bot"

# ------------------------------------------------------------------
# Connectivity check: make sure Windows is reachable before we start.
# ------------------------------------------------------------------
echo "Checking connectivity to Windows machine ($WINDOWS_HOST)..."
if ! ssh -o ConnectTimeout=10 -o BatchMode=yes "$WINDOWS_HOST" "echo ok" >/dev/null 2>&1; then
    echo "Error: cannot reach $WINDOWS_HOST. Is the machine awake?"
    echo "Pipeline aborted."
    exit 1
fi
echo "Windows machine is reachable."

mkdir -p "$PIPELINE_DIR"

echo ""
echo "=== Pipeline started at $(date -u) ==="
echo "Guild: ${TARGET_GUILD_ID}"

# ------------------------------------------------------------------
# Step 1: Export training data via docker exec
# ------------------------------------------------------------------
echo ""
echo "=== Step 1/4: Exporting training data ==="

SINCE_ARG=""
if [ -f "$CURSOR_FILE" ]; then
    SINCE_ARG="--since=$(cat "$CURSOR_FILE")"
    echo "Incremental export since $(cat "$CURSOR_FILE")"
else
    echo "Full export (no previous cursor)"
fi

docker exec "$BOT_CONTAINER" \
    discord-records-bot export \
    --guild-id="$TARGET_GUILD_ID" \
    --output="/data/pipeline/training_data.jsonl" \
    $SINCE_ARG

CONV_COUNT=$(wc -l < "$DATA_FILE" | tr -d ' ')
if [ "$CONV_COUNT" -eq 0 ]; then
    echo "No new conversations to train on. Exiting."
    exit 0
fi
echo "Exported ${CONV_COUNT} conversations."

# ------------------------------------------------------------------
# Step 2: Transfer training data to Windows (small file, SCP is fine)
# ------------------------------------------------------------------
echo ""
echo "=== Step 2/4: Transferring data to training machine ==="

ssh "$WINDOWS_HOST" "if not exist \"${WINDOWS_TRAIN_DIR}\" mkdir \"${WINDOWS_TRAIN_DIR}\""
scp "$DATA_FILE" "${WINDOWS_HOST}:${WINDOWS_TRAIN_DIR}/training_data.jsonl"
echo "Data transferred."

# ------------------------------------------------------------------
# Step 3: Train on Windows
# ------------------------------------------------------------------
echo ""
echo "=== Step 3/4: Training on Windows desktop ==="

ADAPTER_ARG=""
ADAPTER_CHECK=$(ssh "$WINDOWS_HOST" "if exist \"${WINDOWS_TRAIN_DIR}\\output\\last-adapter\\adapter_config.json\" echo exists" 2>/dev/null || true)
if echo "$ADAPTER_CHECK" | grep -q "exists"; then
    ADAPTER_ARG="--adapter ${WINDOWS_TRAIN_DIR}/output/last-adapter"
    echo "Found previous adapter, will do incremental training."
else
    echo "No previous adapter, training from base model."
fi

ssh "$WINDOWS_HOST" "\
    call ${TRAIN_VENV_ACTIVATE} && \
    set PYTHONIOENCODING=utf-8 && \
    python ${TRAIN_SCRIPT_PATH} \
        --data ${WINDOWS_TRAIN_DIR}/training_data.jsonl \
        --output ${WINDOWS_TRAIN_DIR}/output \
        --base-model ${BASE_MODEL} \
        --epochs ${EPOCHS} \
        --quant-method ${QUANT_METHOD} \
        ${ADAPTER_ARG}"

echo "Training complete."

# ------------------------------------------------------------------
# Step 3b: Retrieve GGUF from Windows
#
# Windows OpenSSH SCP truncates large files. Instead we use Python on
# Windows to stream the raw bytes through the SSH connection to stdout.
# ------------------------------------------------------------------
echo ""
echo "Retrieving GGUF from Windows via SSH stream..."

GGUF_SEARCH_DIR="${WINDOWS_TRAIN_DIR}\\output\\gguf_gguf"

GGUF_REMOTE_PATH=$(ssh "$WINDOWS_HOST" \
    "dir /b \"${GGUF_SEARCH_DIR}\\*.gguf\"" 2>/dev/null \
    | tr -d '\r' \
    | grep -i "Q4_K_M" \
    | head -1)

if [ -z "$GGUF_REMOTE_PATH" ]; then
    echo "Not found in gguf_gguf, searching recursively..."
    GGUF_REMOTE_PATH=$(ssh "$WINDOWS_HOST" \
        "dir /s /b \"${WINDOWS_TRAIN_DIR}\\output\\*.gguf\"" 2>/dev/null \
        | tr -d '\r' \
        | grep -i "Q4_K_M" \
        | head -1)
fi

if [ -z "$GGUF_REMOTE_PATH" ]; then
    echo "Error: no Q4_K_M GGUF found under ${WINDOWS_TRAIN_DIR}\\output\\"
    exit 1
fi

GGUF_WIN_PATH="${GGUF_SEARCH_DIR}\\${GGUF_REMOTE_PATH}"
GGUF_WIN_PATH_FWD=$(echo "$GGUF_WIN_PATH" | tr '\\' '/')
echo "Found GGUF: ${GGUF_REMOTE_PATH}"
echo "Streaming ~2 GB through SSH..."

ssh "$WINDOWS_HOST" \
    "${WINDOWS_TRAIN_DIR}/venv/Scripts/python.exe -c \"import sys,shutil;shutil.copyfileobj(open(r'${GGUF_WIN_PATH_FWD}','rb'),sys.stdout.buffer)\"" \
    > "$GGUF_LOCAL"

GGUF_SIZE=$(stat -c%s "$GGUF_LOCAL" 2>/dev/null || stat -f%z "$GGUF_LOCAL" 2>/dev/null)
MIN_SIZE=$((500 * 1024 * 1024))
if [ "$GGUF_SIZE" -lt "$MIN_SIZE" ]; then
    echo "Error: received GGUF is only $(( GGUF_SIZE / 1024 / 1024 )) MB — likely truncated."
    rm -f "$GGUF_LOCAL"
    exit 1
fi
echo "Received GGUF: $(( GGUF_SIZE / 1024 / 1024 )) MB — OK."

# ------------------------------------------------------------------
# Step 4: Deploy to server0
# ------------------------------------------------------------------
echo ""
echo "=== Step 4/4: Deploying to server0 ==="

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
"$SCRIPT_DIR/deploy.sh" "$GGUF_LOCAL" "$BOT_DISPLAY_NAME"

# Save cursor for next incremental export
date -u +"%Y-%m-%dT%H:%M:%SZ" > "$CURSOR_FILE"

echo ""
echo "=== Pipeline complete at $(date -u) ==="
echo "Model: ${MODEL_NAME}"
echo "Conversations trained: ${CONV_COUNT}"
