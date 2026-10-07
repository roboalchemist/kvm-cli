# Security

## Reporting a vulnerability

Please report suspected vulnerabilities privately via GitHub's private
vulnerability reporting:

https://github.com/roboalchemist/kvm-cli/security/advisories/new

Do not open a public issue for security problems. Include reproduction steps,
the affected version (`kvm-cli version`), and relevant logs (secrets redacted).

## Handling of credentials

`kvm-cli` never logs passwords and redacts credentials in its output. Device
credentials are supplied via flags, environment variables, or the config file
(`~/.config/kvm-cli/config.json`, mode `0600`).
