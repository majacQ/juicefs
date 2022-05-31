/*
 * JuiceFS, Copyright 2021 Juicedata, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package meta

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	jsonIndent    = "  "
	jsonWriteSize = 64 << 10
)

type DumpedCounters struct {
	UsedSpace         int64 `json:"usedSpace"`
	UsedInodes        int64 `json:"usedInodes"`
	NextInode         int64 `json:"nextInodes"`
	NextChunk         int64 `json:"nextChunk"`
	NextSession       int64 `json:"nextSession"`
	NextTrash         int64 `json:"nextTrash"`
	NextCleanupSlices int64 `json:"nextCleanupSlices,omitempty"` // deprecated, always 0
}

type DumpedDelFile struct {
	Inode  Ino    `json:"inode"`
	Length uint64 `json:"length"`
	Expire int64  `json:"expire"`
}

type DumpedSustained struct {
	Sid    uint64 `json:"sid"`
	Inodes []Ino  `json:"inodes"`
}

type DumpedAttr struct {
	Inode     Ino    `json:"inode"`
	Type      string `json:"type"`
	Mode      uint16 `json:"mode"`
	Uid       uint32 `json:"uid"`
	Gid       uint32 `json:"gid"`
	Atime     int64  `json:"atime"`
	Mtime     int64  `json:"mtime"`
	Ctime     int64  `json:"ctime"`
	Atimensec uint32 `json:"atimensec,omitempty"`
	Mtimensec uint32 `json:"mtimensec,omitempty"`
	Ctimensec uint32 `json:"ctimensec,omitempty"`
	Nlink     uint32 `json:"nlink"`
	Length    uint64 `json:"length"`
	Rdev      uint32 `json:"rdev,omitempty"`
}

type DumpedSlice struct {
	Chunkid uint64 `json:"chunkid"`
	Pos     uint32 `json:"pos,omitempty"`
	Size    uint32 `json:"size"`
	Off     uint32 `json:"off,omitempty"`
	Len     uint32 `json:"len"`
}

type DumpedChunk struct {
	Index  uint32         `json:"index"`
	Slices []*DumpedSlice `json:"slices"`
}

type DumpedXattr struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type DumpedEntry struct {
	Name    string                  `json:"-"`
	Parents []Ino                   `json:"-"`
	Attr    *DumpedAttr             `json:"attr,omitempty"`
	Symlink string                  `json:"symlink,omitempty"`
	Xattrs  []*DumpedXattr          `json:"xattrs,omitempty"`
	Chunks  []*DumpedChunk          `json:"chunks,omitempty"`
	Entries map[string]*DumpedEntry `json:"entries,omitempty"`
}

var CHARS = []byte("0123456789ABCDEF")

func escape(original string) string {
	// similar to url.Escape but backward compatible if no '%' in it
	var escValue = make([]byte, 0, len(original))
	for i, r := range original {
		if r == utf8.RuneError || r < 32 || r == '%' || r == '"' || r == '\\' {
			if escValue == nil {
				escValue = make([]byte, i, len(original)*2)
				for j := 0; j < i; j++ {
					escValue[j] = original[j]
				}
			}
			c := byte(r)
			if r == utf8.RuneError {
				c = original[i]
			}
			escValue = append(escValue, '%')
			escValue = append(escValue, CHARS[(c>>4)&0xF])
			escValue = append(escValue, CHARS[c&0xF])
		} else if escValue != nil {
			n := utf8.RuneLen(r)
			escValue = append(escValue, original[i:i+n]...)
		}
	}
	if escValue == nil {
		return original
	}
	return string(escValue)
}

func parseHex(c byte) (byte, error) {
	if c >= '0' && c <= '9' {
		return c - '0', nil
	} else if c >= 'A' && c <= 'F' {
		return 10 + (c - 'A'), nil
	} else {
		return 0, fmt.Errorf("hex expected: %c", c)
	}
}

func unescape(s string) []byte {
	if !strings.ContainsRune(s, '%') {
		return []byte(s)
	}

	p := []byte(s)
	n := 0
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '%' && i+2 < len(p) {
			h, e1 := parseHex(p[i+1])
			l, e2 := parseHex(p[i+2])
			if e1 == nil && e2 == nil {
				c = h*16 + l
				i += 2
			}
		}
		p[n] = c
		n++
	}
	return p[:n]
}

func (de *DumpedEntry) writeJSON(bw *bufio.Writer, depth int) error {
	prefix := strings.Repeat(jsonIndent, depth)
	fieldPrefix := prefix + jsonIndent
	write := func(s string) {
		if _, err := bw.WriteString(s); err != nil {
			panic(err)
		}
	}
	write(fmt.Sprintf("\n%s\"%s\": {", prefix, escape(de.Name)))
	data, err := json.Marshal(de.Attr)
	if err != nil {
		return err
	}
	write(fmt.Sprintf("\n%s\"attr\": %s", fieldPrefix, data))
	if len(de.Symlink) > 0 {
		write(fmt.Sprintf(",\n%s\"symlink\": \"%s\"", fieldPrefix, escape(de.Symlink)))
	}
	if len(de.Xattrs) > 0 {
		for _, dumpedXattr := range de.Xattrs {
			dumpedXattr.Value = escape(dumpedXattr.Value)
		}
		if data, err = json.Marshal(de.Xattrs); err != nil {
			return err
		}
		write(fmt.Sprintf(",\n%s\"xattrs\": %s", fieldPrefix, data))
	}
	if len(de.Chunks) == 1 {
		if data, err = json.Marshal(de.Chunks); err != nil {
			return err
		}
		write(fmt.Sprintf(",\n%s\"chunks\": %s", fieldPrefix, data))
	} else if len(de.Chunks) > 1 {
		chunkPrefix := fieldPrefix + jsonIndent
		write(fmt.Sprintf(",\n%s\"chunks\": [", fieldPrefix))
		for i, c := range de.Chunks {
			if data, err = json.Marshal(c); err != nil {
				return err
			}
			write(fmt.Sprintf("\n%s%s", chunkPrefix, data))
			if i != len(de.Chunks)-1 {
				write(",")
			}
		}
		write(fmt.Sprintf("\n%s]", fieldPrefix))
	}
	write(fmt.Sprintf("\n%s}", prefix))
	return nil
}

func (de *DumpedEntry) writeJsonWithOutEntry(bw *bufio.Writer, depth int) error {
	prefix := strings.Repeat(jsonIndent, depth)
	fieldPrefix := prefix + jsonIndent
	write := func(s string) {
		if _, err := bw.WriteString(s); err != nil {
			panic(err)
		}
	}
	write(fmt.Sprintf("\n%s\"%s\": {", prefix, escape(de.Name)))
	data, err := json.Marshal(de.Attr)
	if err != nil {
		return err
	}
	write(fmt.Sprintf("\n%s\"attr\": %s", fieldPrefix, data))
	if len(de.Xattrs) > 0 {
		for _, dumpedXattr := range de.Xattrs {
			dumpedXattr.Value = escape(dumpedXattr.Value)
		}
		if data, err = json.Marshal(de.Xattrs); err != nil {
			return err
		}
		write(fmt.Sprintf(",\n%s\"xattrs\": %s", fieldPrefix, data))
	}
	write(fmt.Sprintf(",\n%s\"entries\": {", fieldPrefix))
	return nil
}

type DumpedMeta struct {
	Setting   Format
	Counters  *DumpedCounters
	Sustained []*DumpedSustained
	DelFiles  []*DumpedDelFile
	FSTree    *DumpedEntry `json:",omitempty"`
	Trash     *DumpedEntry `json:",omitempty"`
}

func (dm *DumpedMeta) writeJsonWithOutTree(w io.Writer) (*bufio.Writer, error) {
	if dm.FSTree != nil || dm.Trash != nil {
		return nil, fmt.Errorf("invalid dumped meta")
	}
	data, err := json.MarshalIndent(dm, "", jsonIndent)
	if err != nil {
		return nil, err
	}
	bw := bufio.NewWriterSize(w, jsonWriteSize)
	if _, err = bw.Write(append(data[:len(data)-2], ',')); err != nil { // delete \n}
		return nil, err
	}
	return bw, nil
}

func dumpAttr(a *Attr) *DumpedAttr {
	d := &DumpedAttr{
		Type:      typeToString(a.Typ),
		Mode:      a.Mode,
		Uid:       a.Uid,
		Gid:       a.Gid,
		Atime:     a.Atime,
		Mtime:     a.Mtime,
		Ctime:     a.Ctime,
		Atimensec: a.Atimensec,
		Mtimensec: a.Mtimensec,
		Ctimensec: a.Ctimensec,
		Nlink:     a.Nlink,
		Rdev:      a.Rdev,
	}
	if a.Typ == TypeFile {
		d.Length = a.Length
	}
	return d
}

func loadAttr(d *DumpedAttr) *Attr {
	return &Attr{
		// Flags:     0,
		Typ:       typeFromString(d.Type),
		Mode:      d.Mode,
		Uid:       d.Uid,
		Gid:       d.Gid,
		Atime:     d.Atime,
		Mtime:     d.Mtime,
		Ctime:     d.Ctime,
		Atimensec: d.Atimensec,
		Mtimensec: d.Mtimensec,
		Ctimensec: d.Ctimensec,
		Nlink:     d.Nlink,
		Rdev:      d.Rdev,
		Full:      true,
	} // Length and Parent not set
}

func collectEntry(e *DumpedEntry, entries map[Ino]*DumpedEntry, showProgress func(totalIncr, currentIncr int64)) error {
	typ := typeFromString(e.Attr.Type)
	inode := e.Attr.Inode
	if showProgress != nil {
		if typ == TypeDirectory {
			showProgress(int64(len(e.Entries)), 1)
		} else {
			showProgress(0, 1)
		}
	}

	if exist, ok := entries[inode]; ok {
		attr := e.Attr
		eattr := exist.Attr
		if typ != TypeFile || typeFromString(eattr.Type) != TypeFile {
			return fmt.Errorf("inode conflict: %d", inode)
		}
		eattr.Nlink++
		exist.Parents = append(exist.Parents, e.Parents...)
		if eattr.Ctime*1e9+int64(eattr.Ctimensec) < attr.Ctime*1e9+int64(attr.Ctimensec) {
			attr.Nlink = eattr.Nlink
			e.Parents = exist.Parents
			entries[inode] = e
		}
		return nil
	}
	entries[inode] = e

	if typ == TypeFile {
		e.Attr.Nlink = 1 // reset
	} else if typ == TypeDirectory {
		if inode == 1 || inode == TrashInode { // root or trash inode
			e.Parents = []Ino{1}
		}
		e.Attr.Nlink = 2
		for name, child := range e.Entries {
			child.Name = name
			child.Parents = []Ino{inode}
			if child.Attr == nil {
				logger.Warnf("ignore empty entry: %s/%s", inode, name)
				continue
			}
			if typeFromString(child.Attr.Type) == TypeDirectory {
				e.Attr.Nlink++
			}
			if err := collectEntry(child, entries, showProgress); err != nil {
				return err
			}
		}
	} else if e.Attr.Nlink != 1 { // nlink should be 1 for other types
		return fmt.Errorf("invalid nlink %d for inode %d type %s", e.Attr.Nlink, inode, e.Attr.Type)
	}
	return nil
}
