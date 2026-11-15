import { configure } from "@testing-library/react";

// The default 1s async-utility timeout is measured while ~8 test files run in
// parallel, so a component that awaits a mocked promise and one React commit
// can miss it on a loaded machine and fail with "Unable to find ..." even
// though nothing is wrong. Two tests in PendingAIIntents.test.tsx failed
// intermittently for exactly that reason (the same file passed in isolation),
// and a flake that only appears under load teaches people to re-run instead of
// read the failure. Waiting longer costs nothing when the condition is already
// true.
configure({ asyncUtilTimeout: 4000 });
