package checksum

import "hash/crc32"

var Table = crc32.MakeTable(crc32.Castagnoli)
