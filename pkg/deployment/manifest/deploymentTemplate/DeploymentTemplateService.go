/*
 * Copyright (c) 2024. Devtron Inc.
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

package deploymentTemplate

import (
	"context"
	"fmt"
	"github.com/devtron-labs/devtron/internal/util"
	bean2 "github.com/devtron-labs/devtron/pkg/auth/user/bean"
	chartRepoRepository "github.com/devtron-labs/devtron/pkg/chartRepo/repository"
	"github.com/devtron-labs/devtron/pkg/deployment/common"
	bean9 "github.com/devtron-labs/devtron/pkg/deployment/common/bean"
	"github.com/devtron-labs/devtron/pkg/deployment/manifest/deploymentTemplate/bean"
	"github.com/devtron-labs/devtron/pkg/deployment/manifest/deploymentTemplate/chartRef"
	bean4 "github.com/devtron-labs/devtron/pkg/deployment/manifest/deploymentTemplate/chartRef/bean"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
	"helm.sh/helm/v3/pkg/chart"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type DeploymentTemplateService interface {
	BuildChartAndGetPath(appName string, envOverride *bean.EnvConfigOverride, envDeploymentConfig *bean9.DeploymentConfig, ctx context.Context) (string, error)
}

type DeploymentTemplateServiceImpl struct {
	logger               *zap.SugaredLogger
	chartRefService      chartRef.ChartRefService
	chartTemplateService util.ChartTemplateService

	chartRepository         chartRepoRepository.ChartRepository
	deploymentConfigService common.DeploymentConfigService
}

func NewDeploymentTemplateServiceImpl(logger *zap.SugaredLogger,
	chartRefService chartRef.ChartRefService,
	chartTemplateService util.ChartTemplateService,
	chartRepository chartRepoRepository.ChartRepository,
	deploymentConfigService common.DeploymentConfigService) *DeploymentTemplateServiceImpl {
	return &DeploymentTemplateServiceImpl{
		logger:                  logger,
		chartRefService:         chartRefService,
		chartTemplateService:    chartTemplateService,
		chartRepository:         chartRepository,
		deploymentConfigService: deploymentConfigService,
	}
}

func (impl *DeploymentTemplateServiceImpl) BuildChartAndGetPath(appName string, envOverride *bean.EnvConfigOverride, envDeploymentConfig *bean9.DeploymentConfig, ctx context.Context) (string, error) {
	if !envDeploymentConfig.IsLinkedRelease() &&
		(!strings.HasSuffix(envOverride.Chart.ChartLocation, fmt.Sprintf("%s%s", "/", envOverride.Chart.ChartVersion)) ||
			!strings.HasSuffix(envDeploymentConfig.GetChartLocation(), fmt.Sprintf("%s%s", "/", envOverride.Chart.ChartVersion))) {
		_, span := otel.Tracer("orchestrator").Start(ctx, "autoHealChartLocationInChart")
		err := impl.autoHealChartLocationInChart(ctx, envOverride, envDeploymentConfig)
		span.End()
		if err != nil {
			return "", err
		}
	}
	chartMetaData := &chart.Metadata{
		Name:    appName,
		Version: envOverride.Chart.ChartVersion,
	}
	referenceTemplatePath := path.Join(bean4.RefChartDirPath, envOverride.Chart.ReferenceTemplate)
	// Load custom charts to referenceTemplatePath if not exists
	if _, err := os.Stat(referenceTemplatePath); os.IsNotExist(err) {
		chartRefValue, err := impl.chartRefService.FindById(envOverride.Chart.ChartRefId)
		if err != nil {
			impl.logger.Errorw("error in fetching ChartRef data", "err", err)
			return "", err
		}
		if chartRefValue.ChartData != nil {
			chartInfo, err := impl.chartRefService.ExtractChartIfMissing(chartRefValue.ChartData, bean4.RefChartDirPath, chartRefValue.Location)
			if chartInfo != nil && chartInfo.TemporaryFolder != "" {
				err1 := os.RemoveAll(chartInfo.TemporaryFolder)
				if err1 != nil {
					impl.logger.Errorw("error in deleting temp dir ", "err", err)
				}
			}
			return "", err
		}
	}
	_, span := otel.Tracer("orchestrator").Start(ctx, "chartTemplateService.BuildChart")
	tempReferenceTemplateDir, err := impl.chartTemplateService.BuildChart(ctx, chartMetaData, referenceTemplatePath)
	span.End()
	if err != nil {
		return "", err
	}
	return tempReferenceTemplateDir, nil
}

func (impl *DeploymentTemplateServiceImpl) autoHealChartLocationInChart(ctx context.Context, envOverride *bean.EnvConfigOverride, envDeploymentConfig *bean9.DeploymentConfig) error {
	chartId := envOverride.Chart.Id
	impl.logger.Infow("auto-healing: Chart location in chart not correct. modifying ", "chartId", chartId,
		"current chartLocation", envOverride.Chart.ChartLocation, "current chartVersion", envOverride.Chart.ChartVersion)

	// get chart from DB (getting it from DB because envOverride.Chart does not have full row of DB)
	_, span := otel.Tracer("orchestrator").Start(ctx, "chartRepository.FindById")
	chart, err := impl.chartRepository.FindById(chartId)
	span.End()
	if err != nil {
		impl.logger.Errorw("error occurred while fetching chart from DB", "chartId", chartId, "err", err)
		return err
	}

	// get chart ref from DB (to get location)
	chartRefId := chart.ChartRefId
	_, span = otel.Tracer("orchestrator").Start(ctx, "chartRefRepository.FindById")
	chartRefDto, err := impl.chartRefService.FindById(chartRefId)
	span.End()
	if err != nil {
		impl.logger.Errorw("error occurred while fetching chartRef from DB", "chartRefId", chartRefId, "err", err)
		return err
	}

	// build new chart location
	newChartLocation := filepath.Join(chartRefDto.Location, envOverride.Chart.ChartVersion)
	impl.logger.Infow("new chart location build", "chartId", chartId, "newChartLocation", newChartLocation)

	// update chart in DB
	chart.ChartLocation = newChartLocation
	_, span = otel.Tracer("orchestrator").Start(ctx, "chartRepository.Update")
	err = impl.chartRepository.Update(chart)
	span.End()
	if err != nil {
		impl.logger.Errorw("error occurred while saving chart into DB", "chartId", chartId, "err", err)
		return err
	}

	// update newChartLocation in model
	envOverride.Chart.ChartLocation = newChartLocation

	//TODO: Ayush review
	envDeploymentConfig.SetChartLocation(newChartLocation)
	envDeploymentConfig, err = impl.deploymentConfigService.CreateOrUpdateConfig(nil, envDeploymentConfig, bean2.SystemUserId)
	if err != nil {
		impl.logger.Errorw("error occurred while creating or updating config", "appId", chart.AppId, "err", err)
		return err
	}

	return nil
}
