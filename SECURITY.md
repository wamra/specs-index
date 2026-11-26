# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability** (private vulnerability reporting). Do not open a public issue for a security problem.

Include what you did, what you expected and what happened, the `specs-index version` output, and your operating system. A minimal document tree that reproduces the problem helps.

## Scope

specs-index indexes document repositories that may not be trusted. Of particular interest:

- a file inside an indexed repository (including `.Wammy/`) changing the embedding provider, enabling a cloud provider, or running a command;
- reading or writing files outside the project root through symbolic links or paths stored in an index;
- document text that reaches a shell or is treated as instructions by the plugin's commands or skill.

See the "Privacy and security" section of the README for the intended behaviour.

## Supported versions

Only the latest release and the `main` branch receive fixes.
