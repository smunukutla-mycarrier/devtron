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

package cluster

import (
	"errors"
	"github.com/devtron-labs/devtron/pkg/cluster/environment/bean"
	"github.com/devtron-labs/devtron/pkg/cluster/repository"
	"go.uber.org/zap"
	"time"
)

type EphemeralContainerService interface {
	AuditEphemeralContainerAction(model bean.EphemeralContainerRequest, actionType repository.ContainerAction) error
}

type EphemeralContainerServiceImpl struct {
	repository repository.EphemeralContainersRepository
	logger     *zap.SugaredLogger
}

func NewEphemeralContainerServiceImpl(repository repository.EphemeralContainersRepository, logger *zap.SugaredLogger) *EphemeralContainerServiceImpl {
	return &EphemeralContainerServiceImpl{
		repository: repository,
		logger:     logger,
	}
}

func (impl *EphemeralContainerServiceImpl) AuditEphemeralContainerAction(model bean.EphemeralContainerRequest, actionType repository.ContainerAction) error {

	container, err := impl.repository.FindContainerByName(model.ClusterId, model.Namespace, model.PodName, model.BasicData.ContainerName)
	if err != nil {
		impl.logger.Errorw("error in finding ephemeral container in the database", "err", err, "ClusterId", model.ClusterId, "Namespace", model.Namespace, "PodName", model.PodName, "ContainerName", model.BasicData.ContainerName)
		return err
	}

	if container != nil && actionType == repository.ActionCreate {
		impl.logger.Errorw("Container already present in the provided pod", "ClusterId", model.ClusterId, "Namespace", model.Namespace, "PodName", model.PodName, "ContainerName", model.BasicData.ContainerName)
		return errors.New("container already present in the provided pod")
	}

	tx, err := impl.repository.StartTx()
	defer func() {
		err = impl.repository.RollbackTx(tx)
		if err != nil {
			impl.logger.Infow("error in rolling back transaction", "err", err, "ClusterId", model.ClusterId, "Namespace", model.Namespace, "PodName", model.PodName, "ContainerName", model.BasicData.ContainerName)
		}
	}()

	if err != nil {
		impl.logger.Errorw("error in creating transaction", "err", err)
		return err
	}

	var auditLogBean repository.EphemeralContainerAction
	if container == nil {
		bean := model.GetContainerBean()
		if actionType != repository.ActionCreate {
			// if a container is not present in database and the user is trying to access/terminate it means it is externally created
			bean.IsExternallyCreated = true
		}
		err = impl.repository.SaveEphemeralContainerData(tx, &bean)
		if err != nil {
			impl.logger.Errorw("Failed to save ephemeral container", "error", err)
			return err
		}
		auditLogBean.EphemeralContainerId = bean.Id
	} else {
		auditLogBean.EphemeralContainerId = container.Id
	}

	auditLogBean.ActionType = actionType
	auditLogBean.PerformedAt = time.Now()
	auditLogBean.PerformedBy = model.UserId

	err = impl.repository.SaveEphemeralContainerActionAudit(tx, &auditLogBean)
	if err != nil {
		impl.logger.Errorw("Failed to save ephemeral container", "error", err)
		return err
	}

	err = impl.repository.CommitTx(tx)
	if err != nil {
		impl.logger.Errorw("error in committing transaction", "err", err, "req", model)
		return err
	}
	impl.logger.Infow("transaction committed successfully", "EphemeralContainerId", auditLogBean.EphemeralContainerId, "ephemeralContainerActionsId", auditLogBean.Id)
	return nil
}
