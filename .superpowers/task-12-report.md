# T12 frontend workspace handoff

## Delivered

- Added capability-driven supplier routes, a route-derived per-tab work mode and per-user default preference. The supplier switcher opens onboarding when eligibility is absent or unknown; active and suspended suppliers can enter the overview. Consumer deep links remain unchanged.
- Added a shared leave confirmation for dirty forms, logout cancellation and stale-response protection, contextual admin navigation, responsive sidebar switcher, localized labels, and separate supplier earnings header placeholder.
- Added onboarding and overview skeletons for T13. These are intentionally not complete supplier application or financial pages. The supplier header displays an em dash until real earnings data is connected.
- Login and OAuth defaults use the `/workspace` redirect, while supplied redirect parameters remain destinations. The preference is the only workspace data persisted locally; credentials and earnings are not persisted.

## Verification

- Initial RED runs: missing store/switcher modules and missing workspace guard helper; a subsequent dirty-form test failed because `confirmLeave` did not exist, and the admin-context test failed before its helper existed.
- Focused workspace/router/sidebar/i18n run: 68 tests passed across six files (before the final adjustment to the existing simulated guard expectations); that guard file was rerun separately with 36 tests passing.
- Full frontend Vitest run: 374 files, 2,924 tests passed. This run began before the final session-race hardening and simulated guard test adjustment; focused tests and typecheck were rerun afterward.
- `vue-tsc --noEmit`: passed after session-race hardening.
- ESLint on changed shell, store, router, APIs and supplier view files: passed. `git diff --check`: passed.
- Browser-based viewport/dark-mode checks were not performed. Supplier application submission, history pages, and actual earnings values belong to T13 and remain unimplemented here.

## Integration notes

- T13 forms can set `useWorkModeStore().dirty` while unsaved; the layout owns the confirmation dialog and the router resolves the pending navigation. Clear it when a form is saved.
- Supplier routes depend on the server capability envelope described in the brief. In particular, suspended profiles must include `supplier` in `allowed_workspaces` for historical access.
- The complete suite emitted existing expected stderr from error-path tests and a Browserslist age notice; there were no test failures in the final run.

## Independent review fixes

- Logout now asks about dirty forms before revoking the session; cancelling preserves both the token and form state. Both header and admin compliance logout paths respect cancellation, and the confirmation appears above the compliance dialog.
- Protected non-admin routes determine consumer mode without a fixed URL list. Unmatched routes are not saved as recent destinations, and the router checks that a remembered destination still exists before switching.
- Re-selecting the current workspace persists the user's preference on a duplicated navigation; cancelled or aborted navigation does not.
- RED: the four review regressions failed before their fixes (logout, nested consumer routes, duplicate selection, unmatched supplier route), and the stale-route fallback failed before validation was added.
- GREEN: focused suite passed 96 tests across seven files; `vue-tsc --noEmit` and ESLint on changed production files passed. The subsequent full Vitest run passed 374 files and 2,931 tests. Expected error-path stderr remains present in the test output.
