# FIX for code quality.

## Dockerfile.web

| line | Issue|
|------|------|
| 7 |  Line is too long. Split it into multiple lines using backslash continuations. 

## cmd/api/main.go

| line | Issue|
|------|------|
| 23 | Refactor this method to reduce its Cognitive Complexity from 38 to the 15 allowed.
| 185 | This function has 8 parameters, which is greater than the 7 authorized.
| 211 | This function has 8 parameters, which is greater than the 7 authorized.
| 478 | Refactor this method to reduce its Cognitive Complexity from 19 to the 15 allowed.
| 543 | Refactor this method to reduce its Cognitive Complexity from 35 to the 15 allowed.
| 626 | Refactor this method to reduce its Cognitive Complexity from 23 to the 15 allowed.

## internal/api/postgres_store.go

| line | Issue|
|------|------|
| 224 | Define a constant instead of duplicating this literal "UPDATE production_lines SET schedule_revision = schedule_revision + 1 WHERE id = $1" 6 times.
| 239 | Refactor this method to reduce its Cognitive Complexity from 20 to the 15 allowed.
| 321 | Refactor this method to reduce its Cognitive Complexity from 16 to the 15 allowed.
| 367 | Refactor this method to reduce its Cognitive Complexity from 19 to the 15 allowed.
| 442 | Refactor this method to reduce its Cognitive Complexity from 34 to the 15 allowed.
| 744 | Refactor this method to reduce its Cognitive Complexity from 22 to the 15 allowed.
| 937 | Refactor this method to reduce its Cognitive Complexity from 29 to the 15 allowed.
| 1110 | Refactor this method to reduce its Cognitive Complexity from 20 to the 15 allowed.
| 1367 | Refactor this method to reduce its Cognitive Complexity from 20 to the 15 allowed.
| 1471 | Refactor this method to reduce its Cognitive Complexity from 22 to the 15 allowed.
| 1562 | Refactor this method to reduce its Cognitive Complexity from 17 to the 15 allowed.

## internal/api/server.go


| line | Issue|
|------|------|
| 1000 | Refactor this method to reduce its Cognitive Complexity from 19 to the 15 allowed.
| 1185 | Refactor this method to reduce its Cognitive Complexity from 23 to the 15 allowed.
| 1336 | Refactor this method to reduce its Cognitive Complexity from 16 to the 15 allowed.
| 1630 | Refactor this method to reduce its Cognitive Complexity from 38 to the 15 allowed.
| 1971 | Refactor this method to reduce its Cognitive Complexity from 69 to the 15 allowed.
| 2226 | Refactor this method to reduce its Cognitive Complexity from 16 to the 15 allowed.
| 2432 | Refactor this method to reduce its Cognitive Complexity from 24 to the 15 allowed.
| 2554 | Refactor this method to reduce its Cognitive Complexity from 28 to the 15 allowed.

## internal/api/server_test.go

| line | Issue|
|------|------|
| 327 | Refactor this method to reduce its Cognitive Complexity from 16 to the 15 allowed.
| 1787 | Refactor this method to reduce its Cognitive Complexity from 16 to the 15 allowed.
| 1996 | Refactor this method to reduce its Cognitive Complexity from 17 to the 15 allowed.

## internal/metrics/metrics_test.go

| line | Issue|
|------|------|
| 139 | Refactor this method to reduce its Cognitive Complexity from 27 to the 15 allowed.

## internal/scheduler/scheduler.go

| line | Issue|
|------|------|
| 71 | Refactor this method to reduce its Cognitive Complexity from 57 to the 15 allowed.

## web/app.js

| line | Issue|
|------|------|
| 422 |  Refactor this function to reduce its Cognitive Complexity from 20 to the 15 allowed.
| 523 | Prefer top-level await over using a promise chain.
| 1949 | `String.raw` should be used to avoid escaping `\`.