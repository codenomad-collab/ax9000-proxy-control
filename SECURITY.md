# Security Policy

## Deployment boundary

Router Proxy Control is intended for a trusted private LAN. Keep the console bound to one router LAN IPv4 address, do not add WAN port forwarding, and do not expose it directly through a public reverse proxy.

The router configuration stores only a random salt and a SHA-256 password hash. Never commit a deployed `config.json`, router backup, SSH credential, Mihomo secret, subscription URL, or diagnostic output that contains private network details.

## Reporting a vulnerability

Use the repository's **Security** tab to report a vulnerability privately when that option is available. Do not include real passwords, access tokens, subscription URLs, public IP addresses, or complete router configuration files in a public issue.

Only the latest version on the default branch is actively maintained.
