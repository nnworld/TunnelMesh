# Task 4 Fix Report

## Contract correction

Metadata API items now always serialize the `redacted` property. Ordinary fields explicitly return `redacted: false`, while sensitive fields continue to return `redacted: true` with the value omitted.

## Verification

- Added a regression assertion for `redacted:false` on ordinary metadata items.
- Confirmed the assertion failed before the JSON tag fix.
- Focused MetadataAPI tests pass after changing the tag to `json:"redacted"`.
