package helper

import (
	"errors"
	"fmt"
	"strings"

	rootcommon "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func ModelMappedHelper(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) error {
	if info.ChannelMeta == nil {
		info.ChannelMeta = &relaycommon.ChannelMeta{}
	}

	// map model name
	modelMapping := c.GetString("model_mapping")
	if modelMapping != "" && modelMapping != "{}" {
		modelMap := make(map[string]string)
		err := rootcommon.Unmarshal([]byte(modelMapping), &modelMap)
		if err != nil {
			return fmt.Errorf("unmarshal_model_mapping_failed")
		}

		// 支持链式模型重定向，最终使用链尾的模型
		currentModel := info.OriginModelName
		visitedModels := map[string]bool{
			currentModel: true,
		}
		for {
			mappedModel, exists := modelMap[currentModel]
			baseModel := hostreasoning.BaseModelName(currentModel)
			if (!exists || mappedModel == "") && baseModel != currentModel {
				mappedModel, exists = modelMap[baseModel]
			}
			if exists && mappedModel != "" {
				// 模型重定向循环检测，避免无限循环
				if visitedModels[mappedModel] {
					if mappedModel == currentModel {
						if currentModel == info.OriginModelName {
							info.IsModelMapped = false
							return nil
						}

						info.IsModelMapped = true
						break
					}
					return errors.New("model_mapping_contains_cycle")
				}
				visitedModels[mappedModel] = true
				currentModel = mappedModel
				info.IsModelMapped = true
			} else {
				break
			}
		}
		if info.IsModelMapped {
			info.UpstreamModelName = currentModel
		}
	}

	if request != nil {
		request.SetModelName(info.UpstreamModelName)
	}
	return nil
}

type ChannelModelMappingResult struct {
	OriginModelName   string
	UpstreamModelName string
	IsModelMapped     bool
}

func ResolveChannelModelMapping(relayMode int, originModelName, modelMapping string) (ChannelModelMappingResult, error) {
	result := ChannelModelMappingResult{
		OriginModelName:   originModelName,
		UpstreamModelName: originModelName,
	}
	if modelMapping != "" && modelMapping != "{}" {
		modelMap := make(map[string]string)
		if err := rootcommon.Unmarshal([]byte(modelMapping), &modelMap); err != nil {
			return ChannelModelMappingResult{}, fmt.Errorf("unmarshal_model_mapping_failed")
		}

		// Preserve channel mapping semantics, including chained mappings and the
		// legacy self-reference behavior used by existing channel configurations.
		currentModel := originModelName
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

	return result, nil
}

func PrepareBillingModelName(c *gin.Context, info *relaycommon.RelayInfo) error {
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
