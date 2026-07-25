# fclaude secrets

SOPS-encrypted secrets, `age`-based (config in [`../.sops.yaml`](../.sops.yaml)). Two recipients can
decrypt: the laptop key and the claude-runner key. Private keys live in
`~/.config/sops/age/keys.txt`, never in the repo.

## `secrets.sops.yaml`

Grafana Cloud Prometheus `remote_write` credentials, consumed by the in-VM metrics agent
(`prometheus --agent`) via `host/bin/fclaude-metrics-setup`:

| key | meaning |
|---|---|
| `gc_prom_url`   | Grafana Cloud Prometheus `remote_write` push URL (not secret) |
| `gc_prom_user`  | numeric instance / stack id (basic-auth username) |
| `gc_prom_token` | access-policy token — **scope `metrics:write` only** (basic-auth password) |

`gc_prom_url` / `gc_prom_user` are copied from `~/projects/vps-config`. Use a **dedicated fclaude**
`metrics:write` access-policy token for `gc_prom_token` (rotatable independently; minimal blast radius
since it lives inside the sandbox VM).

### Edit
```bash
sops secrets/secrets.sops.yaml      # decrypts to $EDITOR, re-encrypts on save
```

### Read one value
```bash
sops -d --extract '["gc_prom_token"]' secrets/secrets.sops.yaml
```
