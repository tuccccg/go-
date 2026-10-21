"""Train/evaluate a PPO routing policy using held-out interaction trajectories."""
from __future__ import annotations
import argparse
import json
from pathlib import Path
import gymnasium as gym
import numpy as np
from gymnasium import spaces
from stable_baselines3 import PPO
from smc import estimate, trajectory_features

class RoutingEnv(gym.Env):
    def __init__(self, rows: list[dict], preference: np.ndarray):
        super().__init__(); self.rows=rows; self.preference=preference.astype(np.float32); self.actions=len(rows[0]["outcomes"]); self.state_dim=len(rows[0]["state"])
        self.action_space=spaces.Discrete(self.actions); self.observation_space=spaces.Box(-np.inf,np.inf,(self.state_dim+self.actions*4,),dtype=np.float32); self.index=0
        self.latency_scale=max(1.0,float(np.percentile([o["latency_ms"] for r in rows for o in r["outcomes"]],90))); self.token_scale=max(1.0,float(np.percentile([o["token_cost"] for r in rows for o in r["outcomes"]],90)))
    def observation(self) -> np.ndarray:
        row=self.rows[self.index]; features=[trajectory_features(o,self.latency_scale,self.token_scale) for o in row["outcomes"]]
        return np.asarray(list(row["state"])+np.asarray(features).flatten().tolist(),dtype=np.float32)
    def reset(self,*,seed=None,options=None):
        super().reset(seed=seed); self.index=int(self.np_random.integers(0,len(self.rows))); return self.observation(),{}
    def step(self,action:int):
        outcome=self.rows[self.index]["outcomes"][int(action)]; features=trajectory_features(outcome,self.latency_scale,self.token_scale)
        return self.observation(),float(np.dot(self.preference,features)),True,False,{"outcome":outcome}

def load_rows(path:Path)->list[dict]:
    rows=[json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
    if not rows: raise ValueError(f"No trajectories found in {path}")
    return rows

def metrics(rows:list[dict],actions:list[int])->dict:
    selected=[row["outcomes"][action] for row,action in zip(rows,actions)]
    return {"completion_rate":float(np.mean([o["completion"] for o in selected])),"tool_success_rate":float(np.mean([o["tool_success"] for o in selected])),"avg_latency_ms":float(np.mean([o["latency_ms"] for o in selected])),"avg_token_cost":float(np.mean([o["token_cost"] for o in selected]))}

def main()->None:
    parser=argparse.ArgumentParser(); parser.add_argument("--data",type=Path,default=Path("data/trajectories.jsonl")); parser.add_argument("--model",type=Path,default=Path("artifacts/ppo-router")); parser.add_argument("--steps",type=int,default=20000); args=parser.parse_args()
    rows=load_rows(args.data); split=max(1,int(len(rows)*0.8)); train,heldout=rows[:split],rows[split:]
    if not heldout: raise ValueError("Need at least 5 trajectories to create a held-out split")
    preference=estimate(train); env=RoutingEnv(train,preference); model=PPO("MlpPolicy",env,verbose=1,seed=7,n_steps=min(128,max(8,len(train)))); model.learn(total_timesteps=args.steps)
    args.model.parent.mkdir(parents=True,exist_ok=True); model.save(str(args.model)); ppo_actions=[]; baseline_actions=[]
    heldout_env=RoutingEnv(heldout,preference); heldout_env.latency_scale=env.latency_scale; heldout_env.token_scale=env.token_scale
    for index,row in enumerate(heldout):
        heldout_env.index=index; action,_=model.predict(heldout_env.observation(),deterministic=True); ppo_actions.append(int(action))
        fixed=np.asarray([0.60,0.25,0.10,0.05]); candidates=[trajectory_features(o,heldout_env.latency_scale,heldout_env.token_scale) for o in row["outcomes"]]; baseline_actions.append(int(np.argmax(np.asarray(candidates)@fixed)))
    report={"preference_weights":preference.tolist(),"heldout_count":len(heldout),"ppo":metrics(heldout,ppo_actions),"rules_baseline":metrics(heldout,baseline_actions)}
    report_path=args.model.parent/"evaluation.json"; report_path.write_text(json.dumps(report,indent=2),encoding="utf-8"); print(json.dumps(report,indent=2))

if __name__ == "__main__": main()
