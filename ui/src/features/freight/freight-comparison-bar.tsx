import { faCodeCompare } from '@fortawesome/free-solid-svg-icons';
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome';
import { Flex, Switch, Typography } from 'antd';
import classNames from 'classnames';
import { generatePath, Link } from 'react-router-dom';

import { paths } from '@ui/config/paths';
import { useGetFreightCreation } from '@ui/features/project/pipelines/freight/use-get-freight-creation';
import { Freight } from '@ui/gen/api/v2/models';

import { getAlias } from '../common/utils';

type FreightComparisonBarProps = {
  className?: string;
  checked: boolean;
  onChange(checked: boolean): void;
  disabled?: boolean;
  // the freight compared against; undefined when the timeline shows no
  // earlier freight from the same warehouse
  previousFreight?: Freight;
};

// FreightComparisonBar is the freight details drawer's switch for the
// highlight-changes option, naming the piece of freight the comparison is
// made against so the diff shown in the artifacts table is unambiguous.
export const FreightComparisonBar = (props: FreightComparisonBarProps) => {
  const previousCreation = useGetFreightCreation(props.previousFreight);

  return (
    <Flex align='center' gap={8} wrap className={classNames(props.className)}>
      <Switch
        size='small'
        checked={props.checked}
        onChange={props.onChange}
        disabled={props.disabled}
      />
      <Typography.Text>
        <FontAwesomeIcon icon={faCodeCompare} className='mr-2 text-gray-500' />
        Highlight changes since previous freight
      </Typography.Text>
      {props.checked &&
        (props.previousFreight ? (
          <Typography.Text type='secondary'>
            compared with{' '}
            <Link
              to={generatePath(paths.freight, {
                name: props.previousFreight.metadata?.namespace,
                freightName: props.previousFreight.metadata?.name
              })}
            >
              {getAlias(props.previousFreight) || props.previousFreight.metadata?.name}
            </Link>
            {previousCreation.relative && (
              <span title={previousCreation.abs?.toString()}>
                {' '}
                (created {previousCreation.relative} ago)
              </span>
            )}
            , the previous freight from {props.previousFreight.origin?.name} visible in the timeline
          </Typography.Text>
        ) : (
          <Typography.Text type='secondary'>
            no earlier freight from this warehouse is visible in the timeline (check its filters)
          </Typography.Text>
        ))}
    </Flex>
  );
};
