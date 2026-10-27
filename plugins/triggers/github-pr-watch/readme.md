# GitHub pull request watch trigger

`github-pr-watch` observes one selected GitHub pull request and supplies bounded
review context to the registered destination. It keeps observing after a
notification; the caller explicitly rearms the same registration with a returned
confirmation position.

The [approved plugin design](../../../docs/design/github-pr-watch.md) defines
selection, authentication, retained history, and coverage limits. The
[rewait control design](../../../docs/design/pr-watch-rewait.md) defines the
shared registration and notification lifecycle.
