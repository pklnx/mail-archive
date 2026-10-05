# Self-hosted CI runner

CI jobs run on a self-hosted GitHub Actions runner (name `vps-ci`, label
`mail-archive-ci`) on a dedicated Debian/Ubuntu x86_64 server.

## Which runner runs a job

| Event | Runner |
|---|---|
| Push to `main`, pull request from a branch of this repository | self-hosted |
| Pull request from a fork | GitHub-hosted (`ubuntu-latest`) |
| Repository variable `CI_RUNNER=github` | GitHub-hosted for everything |

The repository is public. Running fork pull requests on GitHub-hosted runners
means code from outside the repository never runs on our server. In addition,
Settings → Actions → General requires approval before workflows from external
contributors run.

**Emergency switch:** if the server is down, set Settings → Secrets and
variables → Actions → Variables → `CI_RUNNER` to `github` and re-run the
jobs. Delete the variable to switch back.

## Server setup (summary)

1. Update the system; install `ca-certificates curl gnupg git jq make
   build-essential openssl unzip unattended-upgrades ufw`; enable automatic
   security updates.
2. Add 2 GB swap if there is none.
3. `ufw allow OpenSSH && ufw enable` (use your SSH port if it is not 22).
4. Install Docker Engine and the Compose plugin from Docker's apt repository.
5. Create the user `runner` and add it to the `docker` group.
6. As `runner`: download the runner from Settings → Actions → Runners → New
   self-hosted runner (verify the SHA-256 checksum shown there), then
   `./config.sh --url https://github.com/pklnx/mail-archive --token TOKEN --name vps-ci --labels mail-archive-ci --unattended`.
7. As root in `/home/runner/actions-runner`: `./svc.sh install runner && ./svc.sh start`.
8. A weekly systemd timer runs `docker system prune -af --filter until=168h`.

## Security notes

- Docker group membership is root-equivalent. The server must not host
  anything else.
- Docker publishes container ports past `ufw`. Jobs therefore publish service
  ports on `127.0.0.1` only (see `services.postgres.ports` in `ci.yml`).
- The runner only needs outbound HTTPS; no inbound port besides SSH.

## Removing the runner

1. Set `CI_RUNNER=github` (see above) so CI keeps working.
2. On the server, as root: `cd /home/runner/actions-runner && ./svc.sh stop && ./svc.sh uninstall`.
3. As `runner`: `./config.sh remove --token TOKEN` (token from the runner's
   page in Settings → Actions → Runners).
