package sqlitedsn

import "testing"

func TestFileBuildsPortableSQLiteURI(t *testing.T) {
	tests := map[string]struct {
		path string
		want string
	}{
		"windows backslashes": {
			path: `C:\Users\ELAINA\Rin Data\memory.db`,
			want: "file:///C:/Users/ELAINA/Rin%20Data/memory.db",
		},
		"windows forward slashes": {
			path: `D:/Games/Rin/taskstate.db`,
			want: "file:///D:/Games/Rin/taskstate.db",
		},
		"windows UNC": {
			path: `\\server\share\rin\memory.db`,
			want: "file:////server/share/rin/memory.db",
		},
		"unix special characters": {
			path: "/tmp/Rin Data/memory?#.db",
			want: "file:///tmp/Rin%20Data/memory%3F%23.db",
		},
		"unix backslash filename": {
			path: `/tmp/rin\memory.db`,
			want: "file:///tmp/rin%5Cmemory.db",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := File(test.path); got != test.want {
				t.Fatalf("File(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}
