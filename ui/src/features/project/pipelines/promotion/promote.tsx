import { faTruckArrowRight } from '@fortawesome/free-solid-svg-icons';
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome';
import { Alert, Button, Drawer, Flex, Input } from 'antd';
import classNames from 'classnames';
import { useMemo, useState } from 'react';
import { generatePath, Link, useNavigate } from 'react-router-dom';

import { paths } from '@ui/config/paths';
import { useExtensionsContext } from '@ui/extensions/extensions-context';
import { ModalComponentProps } from '@ui/features/common/modal/modal-context';
import { getCurrentFreight } from '@ui/features/common/utils';
import { IAction, useActionContext } from '@ui/features/project/pipelines/context/action-context';
import { Freight, Stage } from '@ui/gen/api/v1alpha1/generated_pb';
import { usePromoteDownstream, usePromoteToStage } from '@ui/gen/api/v2/core/core';

import { useDictionaryContext } from '../context/dictionary-context';
import { isStageControlFlow } from '../nodes/stage-meta-utils';

import {
  autoPromotionHoldStateActive,
  getAutoPromotionCandidateName,
  getAutoPromotionHold,
  originLabel
} from './auto-promotion';
import { FreightDetails } from './freight-details';
import styles from './promote.module.less';
import { useAutoPromotionCandidates } from './use-auto-promotion-candidates';

type PromoteProps = ModalComponentProps & {
  stage: Stage;
  freight: Freight;
};

export const Promote = (props: PromoteProps) => {
  const actionContext = useActionContext();
  const navigate = useNavigate();
  const { promoteTabs } = useExtensionsContext();
  const [reason, setReason] = useState('');

  const dictionaryContext = useDictionaryContext();

  const isDownstreamPromotion =
    actionContext?.action?.type === IAction.PROMOTE_DOWNSTREAM || isStageControlFlow(props.stage);

  const freightAlias = props.freight?.alias;
  const stageName = props.stage?.metadata?.name || '';
  const projectName = props.stage?.metadata?.namespace || '';
  const freightName = props.freight?.metadata?.name || '';

  const currentFreightOnStage = useMemo(() => getCurrentFreight(props.stage)[0], [props.stage]);

  const shouldCheckAutoPromotionCandidate = Boolean(
    projectName && stageName && !isDownstreamPromotion
  );
  const { query: autoPromotionCandidatesQuery, candidates: autoPromotionCandidates } =
    useAutoPromotionCandidates(projectName, stageName, shouldCheckAutoPromotionCandidate);

  const isCheckingAutoPromotionCandidate =
    shouldCheckAutoPromotionCandidate && autoPromotionCandidatesQuery.isLoading;
  const candidateCheckFailed =
    shouldCheckAutoPromotionCandidate && autoPromotionCandidatesQuery.isError;
  const candidateName = getAutoPromotionCandidateName(autoPromotionCandidates, props.freight);
  const candidateFreightPath = candidateName
    ? generatePath(paths.freight, { name: projectName, freightName: candidateName })
    : undefined;
  const candidateFreightLink = candidateFreightPath ? (
    <Link
      to={candidateFreightPath}
      onClick={() => actionContext?.cancel()}
      style={{ overflowWrap: 'anywhere' }}
    >
      {candidateName}
    </Link>
  ) : undefined;
  const selectedOriginLabel = originLabel(props.freight?.origin);
  const isPromotingNonCandidate = Boolean(candidateName && candidateName !== freightName);
  const activeHold = getAutoPromotionHold(props.stage, props.freight?.origin);
  const willResumeOnSuccess = Boolean(
    activeHold?.state === autoPromotionHoldStateActive && candidateName === freightName
  );

  const promoteActionMutation = usePromoteToStage({
    mutation: {
      onSuccess: (response) => {
        if (response.status !== 201) {
          return;
        }
        // navigate
        navigate(
          generatePath(paths.promotion, {
            name: projectName,
            promotionId: response.data?.metadata?.name
          })
        );

        actionContext?.cancel();
      }
    }
  });

  const promoteDownstreamActionMutation = usePromoteDownstream({
    mutation: {
      onSuccess: (response) => {
        if (response.status !== 201) {
          return;
        }
        // navigate
        navigate(
          generatePath(paths.project, {
            name: projectName
          })
        );

        actionContext?.cancel();
      }
    }
  });

  const onPromote = () => {
    if (isDownstreamPromotion) {
      promoteDownstreamActionMutation.mutate({
        project: projectName,
        stage: stageName,
        data: { freight: freightName }
      });
      return;
    }

    promoteActionMutation.mutate({
      stage: stageName,
      project: projectName,
      data: {
        freight: freightName,
        expectedAutoCandidate: candidateName || undefined,
        reason: isPromotingNonCandidate ? reason.trim() || undefined : undefined
      }
    });
  };

  let promotingTo = stageName || '';

  if (isDownstreamPromotion) {
    promotingTo = [...(dictionaryContext?.subscribersByStage?.[promotingTo] || [])].join(', ');
  }

  return (
    <Drawer
      open={props.visible}
      onClose={props.hide}
      title={
        <Flex align='center'>
          Promote {freightAlias} to {promotingTo}
        </Flex>
      }
      size='large'
      width={'1400px'}
      footer={
        <Flex vertical gap={12}>
          {isPromotingNonCandidate && (
            <Input.TextArea
              placeholder='Reason (optional)'
              value={reason}
              onChange={(event) => setReason(event.target.value)}
              maxLength={1024}
              autoSize={{ minRows: 2, maxRows: 4 }}
            />
          )}
          <Button
            size='large'
            className={classNames(styles['promote-btn'])}
            icon={<FontAwesomeIcon icon={faTruckArrowRight} />}
            onClick={onPromote}
            loading={
              isCheckingAutoPromotionCandidate ||
              promoteActionMutation.isPending ||
              promoteDownstreamActionMutation.isPending
            }
            disabled={isCheckingAutoPromotionCandidate || candidateCheckFailed}
          >
            {isCheckingAutoPromotionCandidate
              ? 'Checking auto-promotion'
              : isDownstreamPromotion
                ? 'Promote to downstream'
                : isPromotingNonCandidate
                  ? 'Promote and pause auto-promotion'
                  : 'Promote'}
          </Button>
        </Flex>
      }
    >
      <div className='-mt-6'>
        {isCheckingAutoPromotionCandidate && (
          <Alert
            className='-mx-6'
            banner
            type='info'
            message='Checking the current auto-promotion candidate.'
            description='Promotion is disabled until Kargo can show whether this will pause auto-promotion.'
          />
        )}

        {candidateCheckFailed && (
          <Alert
            className='-mx-6'
            banner
            type='warning'
            message='Could not determine the current auto-promotion candidate.'
            description={`Promoting may pause auto-promotion for ${selectedOriginLabel}.`}
            action={
              <Button size='small' onClick={() => autoPromotionCandidatesQuery.refetch()}>
                Retry
              </Button>
            }
          />
        )}

        {isPromotingNonCandidate && (
          <Alert
            className='-mx-6'
            banner
            type='warning'
            message={
              <>
                This is not the current auto-promotion candidate. Current candidate is{' '}
                {candidateFreightLink}.
              </>
            }
            description={`If this Promotion succeeds, auto-promotion for ${selectedOriginLabel} will pause.`}
          />
        )}

        {willResumeOnSuccess && (
          <Alert
            className='-mx-6'
            banner
            type='info'
            message={`This is the current auto-promotion candidate for ${selectedOriginLabel}.`}
            description='Auto-promotion will resume for this origin if the Promotion succeeds.'
          />
        )}

        <FreightDetails
          freight={props.freight}
          comparison={{ currentFreight: currentFreightOnStage }}
          additionalTabs={promoteTabs.map((data, index) => ({
            children: <data.component freight={props.freight} stage={props.stage} />,
            key: String(data.label + index),
            label: data.label,
            icon: data.icon
          }))}
        />
      </div>
    </Drawer>
  );
};
