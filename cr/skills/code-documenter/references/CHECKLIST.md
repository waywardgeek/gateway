# Expert Documentation Checklist

Use this checklist to verify if a design doc meets the "Expert-Level" standard.

## 1. The "Reimplementation" Test
- [ ] Could I build this in a different language (e.g., Rust or Python) using only this doc?
- [ ] Are all external dependencies and their roles clearly defined?
- [ ] Is the "internal state" of the subsystem fully mapped?

## 2. Architectural Rationale
- [ ] Does it explain why we chose *this* approach over obvious alternatives?
- [ ] Are the trade-offs (performance vs. complexity vs. safety) explicit?

## 3. Concurrency & Safety
- [ ] Are mutexes, channels, or atomic operations explained?
- [ ] Is the locking strategy documented to prevent deadlocks?
- [ ] How are errors propagated and handled?

## 4. Data Lifecycle
- [ ] Where does data enter?
- [ ] Where is it transformed?
- [ ] Where is it persisted or discarded?

## 5. Edge Cases
- [ ] What happens during a crash?
- [ ] What happens if a dependency is slow or unavailable?
- [ ] Are there any "gotchas" in the implementation?
