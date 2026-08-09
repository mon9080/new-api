package helper

import (
	"errors"
	"fmt"
	"strings"

	commonjson "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

type ChannelModelMappingResult struct {
	OriginModelName   string
	UpstreamModelName string
	IsModelMapped     bool
}

func ResolveChannelModelMapping(relayMode int, originModelName, modelMapping string) (ChannelModelMappingResult, error) {
	isResponsesCompact := relayMode == relayconstant.RelayModeResponsesCompact
	mappingModelName := originModelName
	if isResponsesCompact && strings.HasSuffix(originModelName, ratio_setting.CompactModelSuffix) {
		mappingModelName = strings.TrimSuffix(originModelName, ratio_setting.CompactModelSuffix)
	}

	result := ChannelModelMappingResult{
		OriginModelName:   originModelName,
		UpstreamModelName: mappingModelName,
	}
	if modelMapping != "" && modelMapping != "{}" {
		modelMap := make(map[string]string)
		if err := commonjson.Unmarshal([]byte(modelMapping), &modelMap); err != nil {
			return ChannelModelMappingResult{}, fmt.Errorf("unmarshal_model_mapping_failed")
		}

		// Preserve channel mapping semantics, including chained mappings and the
		// legacy self-reference behavior used by existing channel configurations.
		currentModel := mappingModelName
		visitedModels := map[string]bool{
			currentModel: true,
		}
		for {
			mappedModel, exists := modelMap[currentModel]
			if !exists || mappedModel == "" {
				break
			}
			if visitedModels[mappedModel] {
				if mappedModel == currentModel {
					if currentModel == originModelName {
						return result, nil
					}
					result.IsModelMapped = true
					break
				}
				return ChannelModelMappingResult{}, errors.New("model_mapping_contains_cycle")
			}
			visitedModels[mappedModel] = true
			currentModel = mappedModel
			result.IsModelMapped = true
		}
		if result.IsModelMapped {
			result.UpstreamModelName = currentModel
		}
	}

	if isResponsesCompact {
		result.OriginModelName = ratio_setting.WithCompactModelSuffix(result.UpstreamModelName)
	}
	return result, nil
}

func PrepareBillingModelName(c *gin.Context, info *common.RelayInfo) error {
	if info == nil {
		return errors.New("relay_info_is_nil")
	}

	info.BillingModelName = info.OriginModelName
	if c == nil {
		return nil
	}

	resolved, err := ResolveChannelModelMapping(info.RelayMode, info.OriginModelName, c.GetString("model_mapping"))
	if err != nil {
		return err
	}
	if !resolved.IsModelMapped || !hasTieredBillingExpr(resolved.UpstreamModelName) {
		return nil
	}

	info.BillingModelName = resolved.UpstreamModelName
	return nil
}

func ModelMappedHelper(c *gin.Context, info *common.RelayInfo, request dto.Request) error {
	if info.ChannelMeta == nil {
		info.ChannelMeta = &common.ChannelMeta{}
	}

	modelMapping := ""
	if c != nil {
		modelMapping = c.GetString("model_mapping")
	}
	resolved, err := ResolveChannelModelMapping(info.RelayMode, info.OriginModelName, modelMapping)
	if err != nil {
		return err
	}
	info.IsModelMapped = resolved.IsModelMapped
	if resolved.IsModelMapped || info.RelayMode == relayconstant.RelayModeResponsesCompact {
		info.UpstreamModelName = resolved.UpstreamModelName
	}
	info.OriginModelName = resolved.OriginModelName
	if request != nil {
		request.SetModelName(info.UpstreamModelName)
	}
	return nil
}
