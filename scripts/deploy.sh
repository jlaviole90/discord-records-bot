#!/usr/bin/env bash
#
# Deploy a fine-tuned GGUF model to the Ollama instance on server0.
#
# This script:
#   1. Copies the GGUF file to server0's models directory
#   2. Generates an Ollama Modelfile
#   3. Creates (or recreates) the model in Ollama
#   4. Restarts the quotes bot so it detects the new model
#
# Usage (called by pipeline.sh, not directly):
#   ./deploy.sh <gguf_path> <display_name>
#
# Environment variables (set by pipeline.sh):
#   SERVER0, SERVER0_MODELS_DIR, OLLAMA_CONTAINER

set -euo pipefail

SERVER0="${SERVER0:-admin@server0.local}"
SERVER0_MODELS_DIR="${SERVER0_MODELS_DIR:-/home/admin/models}"
OLLAMA_CONTAINER="${OLLAMA_CONTAINER:-discord-quotes-bot-ollama-1}"

GGUF_PATH="${1:?Usage: deploy.sh <gguf_path> <display_name>}"
DISPLAY_NAME="${2:-User}"

MODEL_NAME="impersonate"

if [ ! -f "$GGUF_PATH" ]; then
    echo "Error: GGUF file not found: $GGUF_PATH"
    exit 1
fi

MODEL_DIR="${SERVER0_MODELS_DIR}/${MODEL_NAME}"

echo "==> Creating model directory on ${SERVER0}..."
ssh "$SERVER0" "mkdir -p ${MODEL_DIR}"

echo "==> Copying GGUF to ${SERVER0}:${MODEL_DIR}/model.gguf..."
scp "$GGUF_PATH" "${SERVER0}:${MODEL_DIR}/model.gguf"

echo "==> Generating Modelfile..."
SYSTEM_PROMPT="You are ${DISPLAY_NAME}. You speak exactly as ${DISPLAY_NAME} does in Discord — same vocabulary, slang, humor, opinions, and personality. You ARE ${DISPLAY_NAME}. Respond naturally and concisely as they would in a casual Discord chat."

ssh "$SERVER0" "cat > ${MODEL_DIR}/Modelfile << 'MODELFILE_EOF'
FROM /models/${MODEL_NAME}/model.gguf

SYSTEM \"${SYSTEM_PROMPT}\"

PARAMETER temperature 0.7
PARAMETER top_p 0.9
PARAMETER top_k 40
PARAMETER repeat_penalty 1.3
PARAMETER num_predict 256
MODELFILE_EOF"

echo "==> Creating Ollama model '${MODEL_NAME}'..."
ssh "$SERVER0" "docker exec ${OLLAMA_CONTAINER} ollama create ${MODEL_NAME} -f /models/${MODEL_NAME}/Modelfile"

echo "==> Verifying model is available..."
ssh "$SERVER0" "docker exec ${OLLAMA_CONTAINER} ollama list" | grep "$MODEL_NAME" || {
    echo "Warning: model may not have been created successfully"
    exit 1
}

echo "==> Restarting quotes bot to pick up new model..."
ssh "$SERVER0" "cd ~/discord-quotes-bot && docker compose restart discord-quotes-bot"

echo "==> Done! Model '${MODEL_NAME}' is deployed and the bot has been restarted."
