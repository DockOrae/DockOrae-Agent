package binary

import (
	"context"

	"github.com/moby/moby/client"
)

// mustRemove 删除容器(忽略错误,返回单值供 _ = 使用)
func mustRemove(cli *client.Client, ctx context.Context, id string) error {
	_, err := cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	return err
}
