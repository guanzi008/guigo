// Copyright 2026 肖其顿 (XIAO QI DUN)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package guigo

import (
	"bytes"
	"errors"
	"image/png"
	"io"
	"os"
)

// iconData 读取并校验PNG图标
// 入参: options 窗口选项
// 返回: []byte 图标数据, error 错误信息
func iconData(options Options) ([]byte, error) {
	data := options.Icon
	if options.IconPath != "" {
		file, err := os.Open(options.IconPath)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, (16<<20)+1))
		if err != nil {
			return nil, err
		}
	}
	if len(data) == 0 {
		if options.IconPath != "" {
			return nil, errors.New("empty icon file")
		}
		return nil, nil
	}
	if len(data) > 16<<20 {
		return nil, errors.New("icon exceeds 16 MiB")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width != config.Height || config.Width < 1 || config.Width > 2048 {
		return nil, errors.New("icon must be square and no larger than 2048 pixels")
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return nil, err
	}
	return data, nil
}
