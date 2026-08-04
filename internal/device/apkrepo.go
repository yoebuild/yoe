package device

import "fmt"

// Entries yoe adds to a target's /etc/apk/repositories are bracketed by
// marker comments so they can be rewritten or removed without touching
// anything the user put there. apk-tools 2.x reads that file directly —
// there is no repositories.d/ to drop a separate file into — so the
// markers are the only way to own a region of a shared file.
//
// Writing and removing a block have to agree on the marker text exactly.
// They were written twice, once by the deploy path and once by
// `yoe device repo`, and a change to either spelling would have left
// `device repo remove` unable to find what deploy had written. Both go
// through these helpers now.
const (
	apkRepoBeginFmt = "# >>> yoe-%s"
	apkRepoEndFmt   = "# <<< yoe-%s"
)

// apkRepoStripBlock returns shell that deletes the named block from
// /etc/apk/repositories. Deleting a block that isn't there succeeds, so
// this doubles as the first half of an idempotent write.
func apkRepoStripBlock(name string) string {
	return fmt.Sprintf("sed -i '/^%s$/,/^%s$/d' /etc/apk/repositories\n",
		fmt.Sprintf(apkRepoBeginFmt, name), fmt.Sprintf(apkRepoEndFmt, name))
}

// apkRepoWriteBlock returns shell that replaces the named block with one
// holding entry — the strip above followed by a fresh append, so running
// it twice leaves the file in the same state as running it once.
//
// entry is emitted via `printf '%s\n'` against a single-quoted argument
// rather than interpolated into the heredoc, so a URL containing shell
// metacharacters lands verbatim.
func apkRepoWriteBlock(name, entry string) string {
	return fmt.Sprintf(`mkdir -p /etc/apk
touch /etc/apk/repositories
%s{
    printf '%s\n'
    printf '%%s\n' '%s'
    printf '%s\n'
} >> /etc/apk/repositories
`, apkRepoStripBlock(name),
		fmt.Sprintf(apkRepoBeginFmt, name), entry, fmt.Sprintf(apkRepoEndFmt, name))
}
