#!/usr/bin/env python3
"""
Unsloth QLoRA fine-tuning script for Discord user impersonation.

Trains a LoRA adapter on ShareGPT-formatted conversation data exported
from the records bot's PostgreSQL database, then exports a quantized
GGUF model ready for Ollama deployment.

Usage:
    # First run (from base model):
    python train.py --data training_data.jsonl --output ./output

    # Incremental training (resume from previous adapter):
    python train.py --data new_data.jsonl --output ./output --adapter ./output/last-adapter

    # Custom base model:
    python train.py --data data.jsonl --output ./output --base-model unsloth/tinyllama-bnb-4bit

Requirements:
    pip install unsloth transformers trl datasets peft bitsandbytes
"""

import argparse
import json
import os
import sys

from datasets import Dataset


def parse_args():
    p = argparse.ArgumentParser(description="Fine-tune an LLM to impersonate a Discord user")
    p.add_argument("--data", required=True, help="Path to ShareGPT JSONL training data")
    p.add_argument("--output", default="./output", help="Output directory for adapter and GGUF")
    p.add_argument("--adapter", default=None, help="Path to previous LoRA adapter for incremental training")
    p.add_argument("--base-model", default="unsloth/Llama-3.2-3B-Instruct-bnb-4bit",
                    help="Base model to fine-tune")
    p.add_argument("--epochs", type=int, default=1, help="Number of training epochs")
    p.add_argument("--batch-size", type=int, default=2, help="Per-device train batch size")
    p.add_argument("--grad-accum", type=int, default=4, help="Gradient accumulation steps")
    p.add_argument("--lr", type=float, default=2e-5, help="Learning rate")
    p.add_argument("--max-seq-len", type=int, default=1024, help="Maximum sequence length")
    p.add_argument("--lora-rank", type=int, default=32, help="LoRA rank")
    p.add_argument("--lora-alpha", type=int, default=16, help="LoRA alpha")
    p.add_argument("--quant-method", default="q4_k_m", help="GGUF quantization method")
    p.add_argument("--skip-gguf", action="store_true", help="Skip GGUF export (adapter only)")
    return p.parse_args()


def load_data(path):
    """Load ShareGPT JSONL and return a HuggingFace Dataset."""
    records = []
    with open(path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            records.append(json.loads(line))

    print(f"Loaded {len(records)} conversations from {path}")
    return Dataset.from_list(records)


def format_conversation(example, tokenizer):
    """Convert ShareGPT format to the model's chat template."""
    messages = []
    for turn in example["conversations"]:
        role_map = {"system": "system", "human": "user", "gpt": "assistant"}
        role = role_map.get(turn["from"], turn["from"])
        messages.append({"role": role, "content": turn["value"]})

    text = tokenizer.apply_chat_template(messages, tokenize=False, add_generation_prompt=False)
    return {"text": text}


def main():
    args = parse_args()

    if not os.path.exists(args.data):
        print(f"Error: data file not found: {args.data}", file=sys.stderr)
        sys.exit(1)

    os.makedirs(args.output, exist_ok=True)

    # Lazy import so --help works without GPU
    from unsloth import FastLanguageModel
    from trl import SFTTrainer, SFTConfig

    model_name = args.adapter if args.adapter else args.base_model
    print(f"Loading model: {model_name}")

    model, tokenizer = FastLanguageModel.from_pretrained(
        model_name=model_name,
        max_seq_length=args.max_seq_len,
        dtype=None,
        load_in_4bit=True,
    )

    if not args.adapter:
        print("Applying LoRA adapter to base model...")
        model = FastLanguageModel.get_peft_model(
            model,
            r=args.lora_rank,
            lora_alpha=args.lora_alpha,
            lora_dropout=0,
            target_modules=[
                "q_proj", "k_proj", "v_proj", "o_proj",
                "gate_proj", "up_proj", "down_proj",
            ],
            bias="none",
            use_gradient_checkpointing="unsloth",
        )

    dataset = load_data(args.data)
    dataset = dataset.map(
        lambda ex: format_conversation(ex, tokenizer),
        remove_columns=dataset.column_names,
    )

    adapter_dir = os.path.join(args.output, "last-adapter")
    checkpoint_dir = os.path.join(args.output, "checkpoints")

    trainer = SFTTrainer(
        model=model,
        tokenizer=tokenizer,
        train_dataset=dataset,
        args=SFTConfig(
            dataset_text_field="text",
            max_seq_length=args.max_seq_len,
            packing=True,
            per_device_train_batch_size=args.batch_size,
            gradient_accumulation_steps=args.grad_accum,
            warmup_ratio=0.1,
            num_train_epochs=args.epochs,
            learning_rate=args.lr,
            weight_decay=0.01,
            fp16=True,
            logging_steps=5,
            output_dir=checkpoint_dir,
            save_strategy="epoch",
            optim="adamw_8bit",
            seed=42,
        ),
    )

    print(f"Starting training for {args.epochs} epoch(s)...")
    trainer.train()

    print(f"Saving adapter to {adapter_dir}")
    model.save_pretrained(adapter_dir)
    tokenizer.save_pretrained(adapter_dir)

    if not args.skip_gguf:
        gguf_dir = os.path.join(args.output, "gguf")
        print(f"Exporting GGUF ({args.quant_method}) to {gguf_dir}")
        model.save_pretrained_gguf(
            gguf_dir,
            tokenizer,
            quantization_method=args.quant_method,
        )
        print(f"GGUF export complete: {gguf_dir}")

    print("Training complete.")


if __name__ == "__main__":
    main()
