package interactive

import (
	"context"
	"fmt"
	"github.com/c-bata/go-prompt"
	"github.com/turbot/go-kit/helpers"
	"github.com/turbot/steampipe/pkg/db/db_common"
	"github.com/turbot/steampipe/pkg/steampipeconfig"
	"github.com/turbot/steampipe/pkg/steampipeconfig/modconfig"
	"github.com/turbot/steampipe/pkg/utils"
	"log"
	"strings"
)

func (c *InteractiveClient) initialiseSuggestions(ctx context.Context) error {
	log.Printf("[TRACE] initialiseSuggestions")

	conn, err := c.client().AcquireConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	connectionStateMap, err := steampipeconfig.LoadConnectionState(ctx, conn.Conn(), steampipeconfig.WithWaitUntilLoading())
	if err != nil {
		return err
	}
	// reset suggestions
	c.suggestions = newAutocompleteSuggestions()
	c.initialiseSchemaAndTableSuggestions(connectionStateMap)
	c.initialiseQuerySuggestions()
	c.suggestions.sort()
	return nil
}

// initialiseSchemaAndTableSuggestions build a list of schema and table querySuggestions
func (c *InteractiveClient) initialiseSchemaAndTableSuggestions(connectionStateMap steampipeconfig.ConnectionStateMap) {
	if c.schemaMetadata == nil {
		return
	}

	// unqualified table names
	// use lookup to avoid dupes from dynamic plugins
	// (this is needed as GetFirstSearchPathConnectionForPlugins will return ALL dynamic connections)
	var unqualifiedTablesToAdd = make(map[string]struct{})

	// get the first search path connection for each plugin
	firstConnectionPerPlugin := connectionStateMap.GetFirstSearchPathConnectionForPlugins(c.client().GetRequiredSessionSearchPath())
	firstConnectionPerPluginLookup := utils.SliceToLookup(firstConnectionPerPlugin)
	// NOTE: add temporary schema into firstConnectionPerPluginLookup
	// as we want to add unqualified tables from there into autocomplete
	firstConnectionPerPluginLookup[c.schemaMetadata.TemporarySchemaName] = struct{}{}

	for schemaName, schemaDetails := range c.schemaMetadata.Schemas {
		// fully qualified table names
		var qualifiedTablesToAdd []prompt.Suggest

		isTemporarySchema := schemaName == c.schemaMetadata.TemporarySchemaName
		if !isTemporarySchema {
			// add the schema into the list of schema
			// we don't need to escape schema names, since schema names are derived from connection names
			// which are validated so that we don't end up with names which need it
			c.suggestions.schemas = append(c.suggestions.schemas, prompt.Suggest{Text: schemaName, Description: "Schema", Output: schemaName})
		}

		// add qualified names of all tables
		for tableName := range schemaDetails {
			// do not add temp tables to qualified tables
			if !isTemporarySchema {
				// TODO use description?
				qualifiedTableName := fmt.Sprintf("%s.%s", schemaName, sanitiseTableName(tableName))
				qualifiedTablesToAdd = append(qualifiedTablesToAdd, prompt.Suggest{Text: qualifiedTableName, Description: "Table", Output: qualifiedTableName})
			}
			if _, addToUnqualified := firstConnectionPerPluginLookup[schemaName]; addToUnqualified {
				unqualifiedTablesToAdd[tableName] = struct{}{}
			}
		}

		// add qualified table to tablesBySchema
		if len(qualifiedTablesToAdd) > 0 {
			c.suggestions.tablesBySchema[schemaName] = qualifiedTablesToAdd
		}
	}

	// add unqualified table suggestions
	for tableName := range unqualifiedTablesToAdd {
		c.suggestions.unqualifiedTables = append(c.suggestions.unqualifiedTables, prompt.Suggest{Text: tableName, Description: "Table", Output: sanitiseTableName(tableName)})
	}
}

func (c *InteractiveClient) initialiseQuerySuggestions() {
	workspaceModName := c.initData.Workspace.Mod.Name()
	resourceFunc := func(item modconfig.HclResource) (continueWalking bool, err error) {
		continueWalking = true

		// should we include this item
		qp, ok := item.(modconfig.QueryProvider)
		if !ok {
			return
		}
		if qp.GetQuery() == nil && qp.GetSQL() == nil {
			return
		}
		rm := item.(modconfig.ResourceWithMetadata)
		if rm.IsAnonymous() {
			return
		}
		mod := qp.GetMod()
		isLocal := mod.Name() == workspaceModName
		itemType := item.BlockType()

		// only include global inputs
		if itemType == modconfig.BlockTypeInput {
			if _, ok := c.initData.Workspace.Mod.ResourceMaps.GlobalDashboardInputs[item.Name()]; !ok {
				return
			}
		}
		// special case for query
		if itemType == modconfig.BlockTypeQuery {
			itemType = "named query"
		}
		if isLocal {
			suggestion := c.newSuggestion(itemType, qp.GetDescription(), qp.GetUnqualifiedName())
			c.suggestions.unqualifiedQueries = append(c.suggestions.unqualifiedQueries, suggestion)
		} else {
			suggestion := c.newSuggestion(itemType, qp.GetDescription(), qp.Name())
			c.suggestions.queriesByMod[mod.ShortName] = append(c.suggestions.queriesByMod[mod.ShortName], suggestion)
		}

		return
	}

	c.workspace().GetResourceMaps().WalkResources(resourceFunc)

	// populate mod suggestions
	for mod := range c.suggestions.queriesByMod {
		suggestion := c.newSuggestion("mod", "", mod)
		c.suggestions.mods = append(c.suggestions.mods, suggestion)
	}
}

func sanitiseTableName(strToEscape string) string {
	tokens := helpers.SplitByRune(strToEscape, '.')
	var escaped []string
	for _, token := range tokens {
		// if string contains spaces or special characters(-) or upper case characters, escape it,
		// as Postgres by default converts to lower case
		if strings.ContainsAny(token, " -") || utils.ContainsUpper(token) {
			token = db_common.PgEscapeName(token)
		}
		escaped = append(escaped, token)
	}
	return strings.Join(escaped, ".")
}
