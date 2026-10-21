"""Sequential Monte Carlo estimate of latent reward preferences from feedback."""
from __future__ import annotations
import argparse
import json
from pathlib import Path
import numpy as np

FEATURES = ("completion", "tool_success", "latency_ms", "token_cost")

def trajectory_features(outcome: dict, latency_scale: float, token_scale: float) -> np.ndarray:
    return np.asarray([float(outcome.get("completion", 0)), float(outcome.get("tool_success", 0)),
                       -float(outcome.get("latency_ms", 0))/latency_scale,
                       -float(outcome.get("token_cost", 0))/token_scale], dtype=np.float64)

def estimate(rows: list[dict], particles: int = 4000, seed: int = 7) -> np.ndarray:
    rng = np.random.default_rng(seed)
    latency_scale = max(1.0, float(np.percentile([o["latency_ms"] for r in rows for o in r["outcomes"]], 90)))
    token_scale = max(1.0, float(np.percentile([o["token_cost"] for r in rows for o in r["outcomes"]], 90)))
    observations = []
    for row in rows:
        outcome = row["outcomes"][int(row["action"])]
        feedback = float(row.get("feedback", 1.0))
        observations.append((trajectory_features(outcome, latency_scale, token_scale), feedback >= 0, min(1.0, abs(feedback))))
    cloud = rng.normal([1.0, 0.5, 0.25, 0.25], [0.45, 0.5, 0.35, 0.35], size=(particles, 4))
    cloud[:, :2] = np.maximum(cloud[:, :2], 0)
    log_weights = np.full(particles, -np.log(particles))
    for features, preferred, confidence in observations:
        probability = 1.0/(1.0+np.exp(-np.clip(cloud @ features, -30, 30)))
        likelihood = probability if preferred else 1.0-probability
        log_weights += confidence*np.log(np.maximum(likelihood, 1e-12))
        log_weights -= np.max(log_weights)
        weights = np.exp(log_weights); weights /= weights.sum()
        if 1.0/np.sum(weights**2) < particles*0.55:
            positions = (rng.random()+np.arange(particles))/particles
            cloud = cloud[np.searchsorted(np.cumsum(weights), positions)]
            cloud += rng.normal(0, 0.025, cloud.shape)
            cloud[:, :2] = np.maximum(cloud[:, :2], 0)
            log_weights.fill(-np.log(particles))
    weights = np.exp(log_weights-np.max(log_weights)); weights /= weights.sum()
    posterior = np.average(cloud, axis=0, weights=weights)
    return posterior/max(np.linalg.norm(posterior), 1e-12)

def main() -> None:
    parser=argparse.ArgumentParser(); parser.add_argument("--data",type=Path,default=Path("data/trajectories.jsonl")); parser.add_argument("--output",type=Path,default=Path("data/preference_weights.json")); args=parser.parse_args()
    rows=[json.loads(line) for line in args.data.read_text(encoding="utf-8").splitlines() if line.strip()]
    weights=estimate(rows); result=dict(zip(FEATURES,weights.tolist())); args.output.parent.mkdir(parents=True,exist_ok=True); args.output.write_text(json.dumps(result,indent=2),encoding="utf-8"); print(json.dumps(result,indent=2))

if __name__ == "__main__": main()
