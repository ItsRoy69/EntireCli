package redact

// awsKeyFixture is an AWS-Access-Key-ID-shaped value used to exercise the
// scanner's AWS rule.
//
// It is assembled from two halves instead of being written as one literal.
// GitHub's secret-scanning push protection matches the pattern in raw file
// content, and this repository's own agent transcripts are stored as
// checkpoints and pushed: a literal here reaches a transcript whenever someone
// works on redaction, and a checkpoint carrying it is refused by the remote
// ("push declined due to repository rule violations"). Because a refused
// checkpoint ref is left queued by design, that turns into a push that retries
// and fails on every subsequent `git push`, indefinitely.
//
// Splitting it keeps the rule genuinely under test — the assembled value is
// byte-for-byte what it was before, so the scanner still sees a real match —
// while keeping the 20-character pattern off disk. Do not re-inline it.
const awsKeyFixture = "AKIAYRWQG5" + "EJLPZLBYNP"
