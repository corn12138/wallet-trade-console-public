# Execution registry unit-test ABI fixtures

These minimal, fixed ABI documents are test inputs, not compiled contract artifacts. They contain only the signatures needed to exercise declaration validation and dependency bindings. They contain no bytecode and do not prove that a deployment implements any interface.

Routine Go tests use these committed files so a fresh checkout does not depend on ignored compiler output. `TestBuildWithCompiledContractArtifacts` separately validates real artifact signatures when `EXECUTION_REGISTRY_ARTIFACTS_DIR` points to a compiled artifact directory; an unset directory leaves that evidence explicitly unproven. Web/shared ABI parity checks remain separate.
