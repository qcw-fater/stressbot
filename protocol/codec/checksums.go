// Package codec — checksum 算法实现。
//
// 注册的校验和算法：
//   - none：恒 0（不校验）。
//   - xor8：所有字节 XOR（单字节 bcc）。
//   - sum8：字节累加 mod 256。
//   - crc16：CRC-16/CCITT（多项式 0x1021，初值 0xFFFF，无输入/输出反转）。
//   - crc32：CRC-32/IEEE（标准库 hash/crc32.ChecksumIEEE）。
//   - crc32c：CRC-32/Castagnoli（标准库 hash/crc32.MakeTable(crc32.Castagnoli)）。
package codec

import "hash/crc32"

// noneChecksum 恒返回 0。
type noneChecksum struct{}

func (noneChecksum) Sum(_ []byte, _ map[string]any) uint64 { return 0 }

// xor8Checksum XOR 所有字节（单字节 bcc）。
type xor8Checksum struct{}

func (xor8Checksum) Sum(data []byte, _ map[string]any) uint64 {
	var bcc byte
	for _, b := range data {
		bcc ^= b
	}
	return uint64(bcc)
}

// sum8Checksum 字节累加（mod 256）。
type sum8Checksum struct{}

func (sum8Checksum) Sum(data []byte, _ map[string]any) uint64 {
	var sum byte
	for _, b := range data {
		sum += b
	}
	return uint64(sum)
}

// crc16Checksum CRC-16/CCITT。
// 多项式 0x1021，初始值 0xFFFF，无输入/输出反转。
type crc16Checksum struct{}

func (crc16Checksum) Sum(data []byte, _ map[string]any) uint64 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return uint64(crc)
}

// crc32Checksum CRC-32/IEEE（标准库 hash/crc32.ChecksumIEEE）。
type crc32Checksum struct{}

func (crc32Checksum) Sum(data []byte, _ map[string]any) uint64 {
	return uint64(crc32.ChecksumIEEE(data))
}

// crc32cChecksum CRC-32/Castagnoli（标准库 crc32.Castagnoli 表）。
var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

type crc32cChecksum struct{}

func (crc32cChecksum) Sum(data []byte, _ map[string]any) uint64 {
	return uint64(crc32.Checksum(data, crc32cTable))
}

func init() {
	RegisterChecksum("none", noneChecksum{}, AlgoMeta{
		Description: "恒返回 0（不校验）",
	})
	RegisterChecksum("xor8", xor8Checksum{}, AlgoMeta{
		Description: "所有字节 XOR（单字节 bcc）",
	})
	RegisterChecksum("sum8", sum8Checksum{}, AlgoMeta{
		Description: "字节累加 mod 256",
	})
	RegisterChecksum("crc16", crc16Checksum{}, AlgoMeta{
		Description: "CRC-16/CCITT（多项式 0x1021，初值 0xFFFF，无反转）",
	})
	RegisterChecksum("crc32", crc32Checksum{}, AlgoMeta{
		Description: "CRC-32/IEEE（标准库 hash/crc32.ChecksumIEEE）",
	})
	RegisterChecksum("crc32c", crc32cChecksum{}, AlgoMeta{
		Description: "CRC-32/Castagnoli（iSCSI 多项式）",
	})
}
