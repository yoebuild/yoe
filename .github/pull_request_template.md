<!--
Describe the change in prose: lead with the user-visible benefit (what they can
now do, what was broken and is now fixed), then the how. Skip file/function
names -- those live in the commits.
-->

## Summary

<!--
TUI changes: include screenshots.
CLI changes: paste the terminal output in a fenced code block.
-->

## Checklist

- [ ] Tests pass (`go test ./...`) and a relevant test covers the change
- [ ] Files are formatted (run `yoe_format` in `envsetup.sh`)
- [ ] User-facing changes have a `CHANGELOG` entry and matching `docs/` update
- [ ] TUI changes include screenshots; CLI changes include text output
- [ ] New/changed units were test-built in `testdata/e2e-project/`
- [ ] AI generated code includes specs/plans and listed in
      `docs/SPEC_PLAN_INDEX.md`
