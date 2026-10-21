# Preference learning and PPO routing

The offline pipeline expects JSONL records with `state` (numeric state vector), `action` (observed action index), `feedback` (negative for rejected/poor outcomes), and `outcomes` (one metric object per candidate action). Each outcome has `completion`, `tool_success`, `latency_ms`, and `token_cost`.

`smc.py` estimates latent reward weights with a sequential Monte Carlo posterior. `train_ppo.py` trains a discrete PPO router on the train split and evaluates against a fixed rules baseline on the held-out split.

```powershell
pip install -r requirements.txt
python smc.py --data data/trajectories.jsonl
python train_ppo.py --data data/trajectories.jsonl --steps 20000
```

Split by conversation/session to avoid evaluation leakage. The scripts report measurements from the supplied data; they do not assert the resume's completion, latency, or token-cost gains without a matching held-out run.
