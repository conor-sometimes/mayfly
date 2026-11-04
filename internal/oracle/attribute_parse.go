package oracle

import (
	"encoding/hex"
	"strconv"
	"strings"
)

// AuditExe returns the basename of the program in an audit line's exe= field.
// auditd writes exe= two ways: quoted (exe="/usr/bin/touch"), and hex-encoded
// (exe=2F7573722F...) whenever the path holds a space, quote or control byte.
// Returns "" when exe is absent or "(null)".
func AuditExe(line string) string {
	i := strings.Index(line, " exe=")
	if i < 0 {
		return ""
	}
	v := line[i+len(" exe="):]
	if j := strings.IndexByte(v, ' '); j >= 0 {
		v = v[:j] // the field ends at the next space (a real path with a space is hex-encoded, not here)
	}
	switch {
	case v == "" || v == "(null)":
		return ""
	case strings.HasPrefix(v, `"`):
		v = strings.Trim(v, `"`)
	case isHexStr(v):
		if b, err := hex.DecodeString(v); err == nil {
			v = string(b)
		}
	}
	return v[strings.LastIndexByte(v, '/')+1:]
}

// isHexStr reports whether s is an even-length run of hex digits, auditd's
// encoding for a field value with bytes that cannot appear raw.
func isHexStr(s string) bool {
	if len(s) == 0 || len(s)%2 != 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// AuditProcTree builds a pid -> ppid map from the audit log's SYSCALL records,
// the process tree identity attribution walks. Only SYSCALL records carry both
// pid and ppid; EXECVE/PATH records are keyed by the same event but add no
// parentage. Order does not matter: the map is by pid.
func AuditProcTree(auditLog string) map[int]int {
	tree := make(map[int]int)
	for _, line := range NonEmpty(auditLog) {
		if !strings.Contains(line, "type=SYSCALL") {
			continue
		}
		pid := AuditInt(line, "pid=")
		if pid <= 0 {
			continue
		}
		tree[pid] = AuditInt(line, "ppid=") // ppid may be 0/absent; fine
	}
	return tree
}

// AuditInt reads the integer value of a space-delimited audit field, e.g.
// AuditInt(line, "pid=") -> 1001. It anchors on a leading space so "pid=" does
// NOT match inside "ppid=" (the field before it on the same line): searching
// " pid=" cannot align with " ppid=" because the character before "pid" there
// is 'p', not a space.
func AuditInt(line, key string) int {
	i := strings.Index(line, " "+key)
	if i < 0 {
		if strings.HasPrefix(line, key) {
			i = -1 // key at line start, value begins at len(key)
		} else {
			return 0
		}
	}
	j := i + 1 + len(key) // skip the leading space and the key
	k := j
	for k < len(line) && line[k] >= '0' && line[k] <= '9' {
		k++
	}
	n, _ := strconv.Atoi(line[j:k])
	return n
}
