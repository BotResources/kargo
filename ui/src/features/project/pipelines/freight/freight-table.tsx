import { IconProp } from '@fortawesome/fontawesome-svg-core';
import { faDocker, faGitAlt } from '@fortawesome/free-brands-svg-icons';
import { faAnchor, faArrowDownShortWide } from '@fortawesome/free-solid-svg-icons';
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome';
import { Space, Table, TableProps, theme } from 'antd';
import classNames from 'classnames';
import { useMemo } from 'react';

import { ArtifactMetadata } from '@ui/features/freight/artifact-metadata';
import {
  flattenFreightOrigin,
  TableSource
} from '@ui/features/freight/flatten-freight-origin-utils';
import { Freight } from '@ui/gen/api/v2/models';
import { useLocalStorage } from '@ui/utils/use-local-storage';

import { ArtifactChange, compareFreightArtifacts } from './freight-changed-utils';

// The artifacts-per-page choices offered in the table footer. The pick is
// remembered in the browser, across pieces of freight and sessions.
const FREIGHT_TABLE_PAGE_SIZE_KEY = 'freight-table-page-size';
const PAGE_SIZE_OPTIONS = [5, 10, 20, 50];
const DEFAULT_PAGE_SIZE = 10;

type FreightTableProps = {
  freight: Freight;
  className?: string;
  // when set, each row's version column shows how its artifact differs from
  // this freight: unchanged rows are de-emphasized, changed rows show the
  // previous version struck through next to the current one, new artifacts
  // carry a "new" marker, and artifacts this freight dropped are listed last
  // with a "removed" marker
  previousFreight?: Freight;
};

type FreightTableRow = TableSource & { change?: ArtifactChange };

const versionOf = (record: TableSource): string => {
  switch (record.type) {
    case 'git':
      return record.id;
    case 'helm':
      return record.version;
    case 'image':
      return record.tag || '';
    default:
      return record.version || '-';
  }
};

// a marker for the states the version alone cannot convey, in the same style
// as the freight timeline's "new" marker
const ChangeMarker = ({ children }: { children: string }) => (
  <span className='uppercase opacity-50 text-[10px] font-semibold'>{children}</span>
);

export const FreightTable = (props: FreightTableProps) => {
  const { token } = theme.useToken();

  const [storedPageSize, setPageSize] = useLocalStorage<number>(
    FREIGHT_TABLE_PAGE_SIZE_KEY,
    DEFAULT_PAGE_SIZE
  );
  const pageSize = PAGE_SIZE_OPTIONS.includes(storedPageSize) ? storedPageSize : DEFAULT_PAGE_SIZE;

  const comparing = !!props.previousFreight;

  const rows: FreightTableRow[] = useMemo(
    () =>
      props.previousFreight
        ? compareFreightArtifacts(props.freight, props.previousFreight)
        : flattenFreightOrigin(props.freight),
    [props.freight, props.previousFreight]
  );

  const changedRowBg = `color-mix(in srgb, ${token.colorWarningBg} 50%, ${token.colorBgContainer})`;

  const columns: TableProps<FreightTableRow>['columns'] = [
    {
      title: 'Type',
      width: '10%',
      render: (_, record) => {
        if (record.type === 'other') {
          return record.artifactType || '-';
        }

        let icon: IconProp = faArrowDownShortWide;

        switch (record.type) {
          case 'helm':
            icon = faAnchor;
            break;
          case 'image':
            icon = faDocker;
            break;
          case 'git':
            icon = faGitAlt;
        }

        return <FontAwesomeIcon icon={icon} />;
      }
    },
    {
      title: 'Repo / Name',
      width: '30%',
      render: (_, record) => {
        if (record.type === 'other') {
          return record.subscriptionName || '-';
        }

        return record.repoURL;
      }
    },
    {
      title: 'Version',
      render: (_, record) => {
        const version = versionOf(record);

        switch (record.change?.status) {
          case 'changed':
            return (
              <Space size={6} wrap>
                {record.change.previous && (
                  <>
                    <span className='line-through opacity-50 break-all'>
                      {versionOf(record.change.previous)}
                    </span>
                    <span className='opacity-50'>→</span>
                  </>
                )}
                <span className='break-all'>{version}</span>
              </Space>
            );
          case 'added':
            return (
              <Space size={6} wrap>
                <ChangeMarker>new</ChangeMarker>
                <span className='break-all'>{version}</span>
              </Space>
            );
          case 'removed':
            return (
              <Space size={6} wrap>
                <ChangeMarker>removed</ChangeMarker>
                <span className='line-through opacity-50 break-all'>{version}</span>
              </Space>
            );
          default:
            return version;
        }
      }
    },
    {
      title: 'Metadata',
      width: '600px',
      render: (_, record) => {
        return <ArtifactMetadata {...record} />;
      }
    }
  ];

  return (
    <>
      {rows.length > 0 && (
        <Table<FreightTableRow>
          className={classNames(props.className)}
          pagination={{
            pageSize,
            pageSizeOptions: PAGE_SIZE_OPTIONS,
            showSizeChanger: true,
            onShowSizeChange: (_, size) => setPageSize(size),
            // everything fits on one page even at the smallest size: nothing
            // to page through or resize
            hideOnSinglePage: rows.length <= PAGE_SIZE_OPTIONS[0],
            showTotal: (total) => `${total} artifacts`
          }}
          dataSource={rows}
          rowKey={(_, index) => String(index)}
          rowHoverable={!comparing}
          rowClassName={(record) =>
            classNames({ 'opacity-60': record.change?.status === 'unchanged' })
          }
          onRow={(record) =>
            record.change?.status === 'changed' || record.change?.status === 'added'
              ? { style: { backgroundColor: changedRowBg } }
              : {}
          }
          columns={columns}
        />
      )}
    </>
  );
};
