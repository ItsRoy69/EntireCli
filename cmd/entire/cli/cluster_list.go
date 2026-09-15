package cli

import (
	"cmp"
	"context"
	"slices"

	"github.com/spf13/cobra"

	"github.com/entireio/cli/internal/coreapi"
)

// clusterColumns is the human table view of a cluster. Every column is a value
// some other command takes, which is what the table is for: REGION is the
// jurisdiction slug `org create` and `project create` name with --region,
// CLUSTER is the slug mirror placements are keyed by (`repo mirror list
// --cluster` accepts it), HOST is what `repo create --cluster-host`, `repo
// mirror create` and `repo clone --cluster` take. The catalog's apiUrl and
// isDefault are --json only: the CLI dials the API URL itself, and isDefault
// only separates clusters within a region that has several — `repo get` shows
// where a repo landed regardless.
var clusterColumns = []string{colHeaderRegion, colHeaderCluster, "HOST"}

func clusterRow(cl coreapi.Cluster) []string {
	host, err := hostFromPublicURL(cl.PublicUrl)
	if err != nil {
		host = "-" // unsafe/malformed publicUrl: dashed, never a spoofable host (see clusterHostBySlug)
	}
	return []string{cl.Jurisdiction, cl.Slug, host}
}

// sortClusters orders the catalog for reading — by region, then by slug. The
// server returns registry order.
func sortClusters(clusters []coreapi.Cluster) {
	slices.SortFunc(clusters, func(a, b coreapi.Cluster) int {
		return cmp.Or(
			cmp.Compare(a.Jurisdiction, b.Jurisdiction),
			cmp.Compare(a.Slug, b.Slug),
		)
	})
}

func newClusterListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   cmdList,
		Short: "List the clusters Entire has available",
		Example: "  entire cluster list\n" +
			"  entire cluster list --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCoreList(cmd, "No clusters found.", clusterColumns, clusterRow, func(ctx context.Context, c *coreapi.Client) ([]coreapi.Cluster, error) {
				out, err := c.ListClusters(ctx)
				if err != nil {
					return nil, err
				}
				sortClusters(out.Clusters)
				return out.Clusters, nil
			})
		},
	}
	addJSONFlag(cmd)
	return cmd
}
