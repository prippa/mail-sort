# MailSorter

Cross-platform tool that files mail into folders. Phase 1 can test an IMAP connection and print cleaned messages. It does not move mail. Classification, apply, and the web UI come in later phases.

The spec, dependency pins, and provider notes are in [docs/SPEC.md](docs/SPEC.md). Safety rules are in [AGENTS.md](AGENTS.md).

```bash
make test
make build
./dist/mailsorter-linux-amd64 version
./dist/mailsorter-linux-amd64 test-conn --profile Work
./dist/mailsorter-linux-amd64 dev-clean --profile Work --folder INBOX --limit 5
```

`test-conn` and `dev-clean` read `password_env` from the environment. OAuth is not implemented yet.

Кратко: программа подключается к почте по IMAP и показывает очищенный текст писем. Письма она пока не перемещает. Пароль задаётся переменной окружения, а не файлом настроек.
