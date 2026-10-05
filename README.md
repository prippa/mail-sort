# MailSorter

Cross-platform tool that files mail into folders. It can test an IMAP connection, classify one message, and file a mailbox after a confirmed dry run. The web UI comes in a later phase.

The spec, dependency pins, and provider notes are in [docs/SPEC.md](docs/SPEC.md). Safety rules are in [AGENTS.md](AGENTS.md).

```bash
make test
make build
./dist/mailsorter-linux-amd64 version
./dist/mailsorter-linux-amd64 test-conn --profile Work
./dist/mailsorter-linux-amd64 dev-clean --profile Work --folder INBOX --limit 5
./dist/mailsorter-linux-amd64 categories export > categories.yaml
./dist/mailsorter-linux-amd64 classify --stdin < message.json
./dist/mailsorter-linux-amd64 run --profile Work
./dist/mailsorter-linux-amd64 run --profile Work --confirm
./dist/mailsorter-linux-amd64 run --profile Work --apply
./dist/mailsorter-linux-amd64 undo --profile Work
```

`test-conn` and `dev-clean` read `password_env` from the environment. `classify` reads `TYPESAFE_API_KEY` for Jev, or the variable named by `key_env` for another provider. OAuth is not implemented yet. With no `categories.yaml`, classify uses the starter list and does not call a provider until `classifiers` is set.

Кратко: программа подключается к почте по IMAP, классифицирует письма и после подтверждения раскладывает их по папкам. Первый запуск профиля только показывает план. Пароль и ключ API задаются переменными окружения, а не файлом настроек.
