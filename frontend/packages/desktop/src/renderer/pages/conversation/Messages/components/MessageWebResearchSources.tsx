import React, { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { extractResearchSourcePresentation, isResearchActivityTool } from './researchSourcePresentation';

export { isResearchActivityTool } from './researchSourcePresentation';

const MessageWebResearchSources: React.FC<{
  toolName: string;
  input?: string;
  output?: string;
}> = ({ toolName, input, output }) => {
  const { t } = useTranslation();
  const presentation = useMemo(
    () =>
      isResearchActivityTool(toolName)
        ? extractResearchSourcePresentation(input, output)
        : { query: null, results: [] },
    [input, output, toolName]
  );

  const sourceLabel = t('messages.researchSources');
  return (
    <section className='tool-research-sources' aria-label={sourceLabel}>
      {presentation.query ? (
        <div className='tool-research-sources__query'>
          <span>{t('messages.researchQuery')}</span>
          <span className='tool-research-sources__query-text'>{presentation.query}</span>
        </div>
      ) : null}
      {presentation.results.length > 0 ? (
        <div className='tool-research-sources__results'>
          {presentation.results.map((source) => (
            <article key={source.key} className='tool-research-source'>
              {source.url ? (
                <a href={source.url} target='_blank' rel='noopener noreferrer' className='tool-research-source__title'>
                  {source.title}
                </a>
              ) : (
                <div className='tool-research-source__title'>{source.title}</div>
              )}
              {source.source ? <div className='tool-research-source__source'>{source.source}</div> : null}
              {source.snippet ? <p className='tool-research-source__snippet'>{source.snippet}</p> : null}
            </article>
          ))}
        </div>
      ) : (
        <p className='tool-research-sources__empty'>{t('messages.researchEmpty')}</p>
      )}
    </section>
  );
};

export default MessageWebResearchSources;
