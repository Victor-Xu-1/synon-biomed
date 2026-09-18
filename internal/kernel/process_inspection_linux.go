//go:build linux

package kernel

import (
	"bufio"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const processWalkLimit = 4096

type linuxObservedCounter struct {
	process ObservedProcess
	start   uint64
	cpu     uint64
	rss     uint64
}

func readLinuxObservedCounter(root string, pid int) (linuxObservedCounter, error) {
	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return linuxObservedCounter{}, err
	}
	open, close := strings.IndexByte(string(raw), '('), strings.LastIndex(string(raw), ") ")
	if open < 1 || close <= open {
		return linuxObservedCounter{}, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(raw[close+2:]))
	if len(fields) < 22 {
		return linuxObservedCounter{}, errors.New("incomplete process stat")
	}
	parent, parentErr := strconv.Atoi(fields[1])
	user, userErr := strconv.ParseUint(fields[11], 10, 64)
	system, systemErr := strconv.ParseUint(fields[12], 10, 64)
	start, startErr := strconv.ParseUint(fields[19], 10, 64)
	rss, rssErr := strconv.ParseInt(fields[21], 10, 64)
	if parentErr != nil || userErr != nil || systemErr != nil || startErr != nil || rssErr != nil || start == 0 {
		return linuxObservedCounter{}, errors.New("invalid process counters")
	}
	name, source := string(raw[open+1:close]), "process_name"
	if path, err := os.Readlink(filepath.Join(root, strconv.Itoa(pid), "exe")); err == nil {
		name, source = filepath.Base(strings.TrimSuffix(path, " (deleted)")), "executable"
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	if len([]rune(name)) > 255 {
		name = string([]rune(name)[:255])
	}
	result := linuxObservedCounter{start: start, cpu: user + system, process: ObservedProcess{
		PID: pid, ParentPID: parent, StartIdentity: strconv.FormatUint(start, 10), Name: name, NameSource: source, State: fields[0],
	}}
	if rss > 0 && uint64(rss) <= math.MaxUint64/uint64(os.Getpagesize()) {
		result.rss = uint64(rss) * uint64(os.Getpagesize())
	}
	return result, nil
}

// Read every thread's children. Cancellation, recovery and resource observation
// share the same process membership boundary, including non-leader threads.
func linuxObservedChildren(root string, pid int, budget *int) ([]int, bool) {
	childLimit := *budget
	directory, err := os.Open(filepath.Join(root, strconv.Itoa(pid), "task"))
	if err != nil {
		return nil, false
	}
	defer directory.Close()
	children := map[int]struct{}{}
	complete := true
	for {
		threads, readErr := directory.Readdirnames(64)
		for _, thread := range threads {
			if *budget <= 0 {
				return sortedProcessIDs(children), false
			}
			*budget--
			if _, err := strconv.Atoi(thread); err != nil {
				continue
			}
			file, err := os.Open(filepath.Join(root, strconv.Itoa(pid), "task", thread, "children"))
			if err != nil {
				complete = false
				continue
			}
			scanner := bufio.NewScanner(file)
			scanner.Split(bufio.ScanWords)
			scanner.Buffer(make([]byte, 64), 64)
			for scanner.Scan() {
				id, err := strconv.Atoi(scanner.Text())
				if err == nil && id > 0 {
					children[id] = struct{}{}
				} else {
					complete = false
				}
				if len(children) >= childLimit {
					_ = file.Close()
					return sortedProcessIDs(children), false
				}
			}
			if scanner.Err() != nil {
				complete = false
			}
			_ = file.Close()
		}
		if readErr != nil {
			return sortedProcessIDs(children), complete && errors.Is(readErr, io.EOF)
		}
	}
}

func sortedProcessIDs(ids map[int]struct{}) []int {
	result := make([]int, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Ints(result)
	return result
}

func readLinuxProcessTreeMembersAt(root string, pid int, expectedStart uint64, limit int) ([]linuxObservedCounter, bool, error) {
	if pid <= 0 {
		return nil, false, os.ErrNotExist
	}
	initial, err := readLinuxObservedCounter(root, pid)
	if err != nil {
		return nil, false, err
	}
	if expectedStart != 0 && initial.start != expectedStart || !linuxProcessStateLive(initial.process.State) {
		return nil, false, os.ErrNotExist
	}
	members := []linuxObservedCounter{}
	partial := false
	queue := []linuxObservedCounter{initial}
	seen := map[int]bool{pid: true}
	budget := limit
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		current, err := readLinuxObservedCounter(root, parent.process.PID)
		if err != nil || current.start != parent.start || current.process.ParentPID != parent.process.ParentPID {
			partial = true
			continue
		}
		if !linuxProcessStateLive(current.process.State) {
			continue
		}
		members = append(members, current)
		children, complete := linuxObservedChildren(root, current.process.PID, &budget)
		if !complete {
			partial = true
		}
		for _, child := range children {
			if seen[child] {
				continue
			}
			if len(seen) >= limit {
				partial = true
				break
			}
			seen[child] = true
			value, err := readLinuxObservedCounter(root, child)
			if err != nil {
				partial = true
				continue
			}
			if value.process.ParentPID != current.process.PID || value.start < current.start {
				partial = true
				continue
			}
			queue = append(queue, value)
		}
	}
	// If the root disappeared or its PID was reused mid-sample, no descendant
	// can be attributed to this execution even if its own stat was readable.
	final, err := readLinuxObservedCounter(root, pid)
	if err != nil {
		return nil, false, err
	}
	if final.start != initial.start || !linuxProcessStateLive(final.process.State) {
		return nil, false, os.ErrNotExist
	}
	return members, partial, nil
}

func linuxProcessStateLive(state string) bool {
	return state != "Z" && state != "X" && state != "x"
}
