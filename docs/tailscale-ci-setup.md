# Tailscale setup for scheduled CI builds

The scheduled workflow in `dio/envoy-builder` runs on a GitHub-hosted runner
and SSHes to your Mac mini over Tailscale to do the actual Envoy build.
This doc covers everything you need to configure on the Tailscale side and in
GitHub before the workflow can run.

---

## 1. Mac mini: install and enroll Tailscale

If Tailscale is not already running on the mini:

```sh
brew install tailscale
sudo tailscaled install-system-daemon   # installs a launchd plist, survives reboots
tailscale up
```

After `tailscale up` your browser opens for login. Complete it.

Confirm the mini is enrolled:

```sh
tailscale status
```

You should see the mini listed with a `100.x.y.z` address.

### Set a stable machine name (optional but recommended)

In the Tailscale admin console (https://login.tailscale.com/admin/machines),
click the mini → "Edit machine name" → set it to something short like `mini`.
With MagicDNS enabled the hostname resolves as `mini.<tailnet-name>.ts.net`
and, within the tailnet, just `mini`.

You will use this name as `--host dio@mini` in the workflow.

---

## 2. Tailscale admin: enable MagicDNS

https://login.tailscale.com/admin/dns

- Toggle "MagicDNS" on.
- This lets nodes resolve each other by short name (`mini`) inside the tailnet.
  Without it you have to use the raw `100.x.y.z` IP in `--host`.

---

## 3. Tailscale admin: add a `tag:ci` ACL tag

Tags let you grant CI ephemeral nodes only the access they need.

Go to https://login.tailscale.com/admin/acls and edit the policy JSON.
Add the tag owner and an ACL rule that lets `tag:ci` nodes reach the mini:

```json
{
  "tagOwners": {
    "tag:ci": ["autogroup:admin"]
  },

  "acls": [
    // ... your existing rules ...

    // allow CI runners to reach the mini on SSH port
    {
      "action": "accept",
      "src":    ["tag:ci"],
      "dst":    ["mini:22"]
    }
  ]
}
```

What this does:
- `tagOwners` declares who can issue auth keys that carry `tag:ci`.
  `autogroup:admin` means any admin of your tailnet can create such keys —
  including the OAuth client you create next.
- The ACL rule lets any node tagged `tag:ci` connect to port 22 on `mini`.
  Adjust the destination if your SSH port differs.

Save the policy. Tailscale validates the JSON before accepting it.

---

## 4. Tailscale admin: create an OAuth client

OAuth clients are how GitHub Actions authenticates to Tailscale and creates
short-lived ephemeral auth keys on the fly.

Go to https://login.tailscale.com/admin/settings/oauth

Click "Generate OAuth client".

Settings:
- **Description**: `envoy-builder CI`
- **Scopes**: check `Auth Keys` → `Write`
  (this lets the client create ephemeral auth keys tagged `tag:ci`)
- **Tags**: add `tag:ci`

Click "Generate client". You get a `client_id` and a `client_secret`.
Copy both — the secret is shown only once.

These become the GH secrets `TS_OAUTH_CLIENT_ID` and `TS_OAUTH_SECRET`.

---

## 5. SSH key for the mini

The GitHub runner needs to authenticate to the mini over SSH.
The simplest approach: use an existing key that the mini already trusts,
or generate a dedicated CI key.

### Option A: reuse your existing key

If your personal key (`~/.ssh/id_ed25519`) is already in
`~/.ssh/authorized_keys` on the mini:

```sh
cat ~/.ssh/id_ed25519   # copy this — it becomes the GH secret MINI_SSH_KEY
```

### Option B: generate a dedicated CI key (recommended)

```sh
ssh-keygen -t ed25519 -C "envoy-builder-ci" -f ~/.ssh/envoy_builder_ci -N ""
# Add the public key to the mini:
ssh-copy-id -i ~/.ssh/envoy_builder_ci.pub dio@mini
# Or manually:
cat ~/.ssh/envoy_builder_ci.pub | ssh dio@mini 'cat >> ~/.ssh/authorized_keys'
```

The private key (`~/.ssh/envoy_builder_ci`) becomes the GH secret `MINI_SSH_KEY`.

Verify from your workstation that it works:

```sh
ssh -i ~/.ssh/envoy_builder_ci dio@mini uname -m
# should print: arm64
```

---

## 6. GitHub secrets

In the `dio/envoy-builder` repo → Settings → Secrets and variables → Actions,
add the following repository secrets:

| Secret name                      | Value                                              |
|----------------------------------|----------------------------------------------------|
| `TS_OAUTH_CLIENT_ID`             | OAuth client ID from step 4                        |
| `TS_OAUTH_SECRET`                | OAuth client secret from step 4                    |
| `MINI_SSH_KEY`                   | Private key content from step 5 (entire PEM block) |
| `BUILDBUDDY_API_KEY_DARWIN_ARM64`| BuildBuddy key for macOS builds                    |
| `BUILDBUDDY_API_KEY_LINUX_ARM64` | BuildBuddy key for Linux arm64 builds              |
| `BUILDBUDDY_API_KEY_LINUX_AMD64` | BuildBuddy key for Linux amd64 builds              |

`GITHUB_TOKEN` is provided automatically — no action needed.

---

## 7. Verify end-to-end before the workflow

From your workstation, confirm the mini is reachable by name inside the tailnet:

```sh
tailscale ping mini
ssh dio@mini echo ok
```

From a machine outside your local network (e.g. after `tailscale down` on
your laptop to simulate a remote runner), check that `tailscale ping mini`
still resolves and SSH still works via the Tailscale IP.

---

## 8. Workflow (for reference)

The actual workflow lives in `dio/envoy-builder`. This is what it uses:

```yaml
- name: Connect to Tailscale
  uses: tailscale/github-action@v3
  with:
    oauth-client-id: ${{ secrets.TS_OAUTH_CLIENT_ID }}
    oauth-secret: ${{ secrets.TS_OAUTH_SECRET }}
    tags: tag:ci

- name: Set up SSH key
  run: |
    mkdir -p ~/.ssh
    printf '%s\n' "${{ secrets.MINI_SSH_KEY }}" > ~/.ssh/id_ed25519
    chmod 600 ~/.ssh/id_ed25519

- name: Build
  run: |
    envoy-mini-builder build \
      --sha "${{ github.event.inputs.sha || 'main' }}" \
      --host dio@mini \
      --all-platforms \
      --gh-repo dio/envoy-builder
  env:
    GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
    BUILDBUDDY_API_KEY: ${{ secrets.BUILDBUDDY_API_KEY }}
```

`tailscale/github-action@v3` joins the runner to your tailnet as an ephemeral
`tag:ci` node and exits cleanly at the end of the job (the node is removed from
your tailnet automatically).

---

## Troubleshooting

**SSH times out from the runner**
- Check the ACL rule in step 3 allows `tag:ci` → `mini:22`.
- Check `tailscale status` on the mini shows it connected.
- Make sure `tailscaled` is running as a daemon (step 1) and not just a
  user-level process that logged out.

**`tailscale: command not found` on the mini**
- Reinstall via brew and re-run `sudo tailscaled install-system-daemon`.

**MagicDNS does not resolve `mini`**
- Confirm MagicDNS is enabled in the admin console (step 2).
- Use the raw Tailscale IP (`100.x.y.z`) in `--host` as a fallback:
  `--host dio@100.x.y.z`.

**OAuth client error: "tag not permitted"**
- The tag in the OAuth client settings (step 4) must match the tag in the
  ACL `tagOwners` map (step 3). Both must say `tag:ci`.

**Build fails immediately with "Host key verification failed"**
- The `sshArgs` in `builder.go` already passes `-o StrictHostKeyChecking=accept-new`
  so first-connection host key acceptance is automatic. If this error still
  appears, it means a stale known_hosts entry conflicts. Clear it:
  `ssh-keygen -R mini` on the runner (add a workflow step before the build).
